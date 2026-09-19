package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/activity"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/auth"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/config"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/policy"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/vault"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/web"
)

type Server struct {
	awake               awake
	localMu             sync.Mutex
	localTickets        map[string]time.Time
	config              config.Config
	store               *store.Store
	activity            activity.Store
	issuer              *auth.Issuer
	logger              *slog.Logger
	vault               *vault.Vault
	engines             *engine.Manager
	txnMu               sync.Mutex
	txns                map[string]*transactionEntry
	mongoTxnMu          sync.Mutex
	mongoTxns           map[string]*mongoTxnEntry
	stopTransactions    context.CancelFunc
	topologyMu          sync.Mutex
	topologyLocks       map[string]*sync.Mutex
	stopTopology        context.CancelFunc
	rateMu              sync.Mutex
	rateClients         map[string]*rateWindow
	statementHooks      []statementHook
	resultHooks         []resultHook
	routeRegistrars     []RouteRegistrar
	escalation          Escalation
	access              Access
	connectionDetails   []func(context.Context, domain.Connection, map[string]any)
	connectionSaveHooks []ConnectionSaveHook
	connectionFields    map[string]bool
	scheduleMu          sync.Mutex
	scheduleRunning     map[string]bool
	scheduleContext     context.Context
	stopSchedules       context.CancelFunc
	scheduleRuns        sync.WaitGroup
	importMu            sync.Mutex
	stopRetention       context.CancelFunc
	stopAuditVerify     context.CancelFunc
	imports             map[string]*csvUpload
}

func New(cfg config.Config, data *store.Store, issuer *auth.Issuer, logger *slog.Logger) *Server {
	// Statements are recorded by one background writer in batches, so an
	// audit write never holds up the statement it describes.
	return NewWithActivity(cfg, data, activity.NewBuffered(activity.SQLite{Data: data}, 4096), issuer, logger)
}

func NewWithActivity(cfg config.Config, data *store.Store, activityStore activity.Store, issuer *auth.Issuer, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	secretVault, _ := vault.New(cfg.EncryptionKey, cfg.EncryptionKeyPrevious)
	txnContext, cancel := context.WithCancel(context.Background())
	topologyContext, stopTopology := context.WithCancel(context.Background())
	server := &Server{config: cfg, store: data, activity: activityStore, issuer: issuer, logger: logger, vault: secretVault, engines: engine.NewManager(), txns: make(map[string]*transactionEntry), mongoTxns: make(map[string]*mongoTxnEntry), stopTransactions: cancel, topologyLocks: make(map[string]*sync.Mutex), stopTopology: stopTopology, rateClients: make(map[string]*rateWindow)}
	go server.transactionReaper(txnContext)
	go server.topologyRefresher(topologyContext)
	retentionContext, stopRetention := context.WithCancel(context.Background())
	server.stopRetention = stopRetention
	// The periods are settled here, before the loop starts, so it never reads
	// configuration another goroutine could still be setting up.
	auditDays, historyDays := server.retentionPeriods()
	go server.activityRetention(retentionContext, auditDays, historyDays)
	auditVerifyContext, stopAuditVerify := context.WithCancel(context.Background())
	server.stopAuditVerify = stopAuditVerify
	go server.auditIntegrityCheck(auditVerifyContext)
	server.scheduleRunning = map[string]bool{}
	server.imports = map[string]*csvUpload{}
	server.scheduleContext, server.stopSchedules = context.WithCancel(context.Background())
	// Scheduled queries write files on this computer, so only personal
	// workspaces run them.
	if !cfg.Shared {
		go server.scheduler(server.scheduleContext)
	}
	return server
}

func (s *Server) Close() error {
	s.stopTransactions()
	s.stopTopology()
	if s.stopSchedules != nil {
		s.stopSchedules()
		s.scheduleRuns.Wait()
	}
	s.importMu.Lock()
	for key, item := range s.imports {
		_ = os.Remove(item.path)
		delete(s.imports, key)
	}
	s.importMu.Unlock()
	s.txnMu.Lock()
	items := make([]*transactionEntry, 0, len(s.txns))
	for id, item := range s.txns {
		items = append(items, item)
		delete(s.txns, id)
	}
	s.txnMu.Unlock()
	for _, item := range items {
		item.mu.Lock()
		_ = item.transaction.Rollback()
		item.mu.Unlock()
	}
	s.mongoTxnMu.Lock()
	mongoItems := make([]*mongoTxnEntry, 0, len(s.mongoTxns))
	for id, item := range s.mongoTxns {
		mongoItems = append(mongoItems, item)
		delete(s.mongoTxns, id)
	}
	s.mongoTxnMu.Unlock()
	for _, item := range mongoItems {
		item.mu.Lock()
		_ = item.transaction.Rollback()
		item.mu.Unlock()
	}
	if s.stopRetention != nil {
		s.stopRetention()
	}
	if s.stopAuditVerify != nil {
		s.stopAuditVerify()
	}
	// Queued activity is written before the store closes behind the server.
	if closer, ok := s.activity.(interface{ Close() }); ok {
		closer.Close()
	}
	return s.engines.Close()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	if !s.config.Shared && s.config.LocalLauncherKey != "" {
		mux.HandleFunc("POST /api/local/open", s.localOpen)
		mux.HandleFunc("POST /api/local/stop", s.localStop)
		mux.HandleFunc("POST /api/auth/local", s.localLogin)
		mux.Handle("POST /api/local/quit", s.requireAdmin(http.HandlerFunc(s.localQuit)))
	}
	mux.HandleFunc("GET /api/meta/instance", func(w http.ResponseWriter, _ *http.Request) {
		mode := "personal"
		if s.config.Shared {
			mode = "shared"
		}
		writeJSON(w, http.StatusOK, map[string]any{"mode": mode, "desktop": s.config.LocalLauncherKey != "", "duckdb": engine.DuckDBAvailable, "engineCapabilities": engine.AllEngineCapabilities()})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /api/meta/engines", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"engines": []string{"postgres", "mysql", "mariadb", "mssql"}, "capabilities": engine.AllEngineCapabilities()})
	})
	mux.HandleFunc("POST /api/auth/refresh", s.refresh)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	mux.Handle("GET /api/auth/me", s.authenticated(http.HandlerFunc(s.me)))
	mux.Handle("GET /api/connections", s.authenticated(http.HandlerFunc(s.listConnections)))
	mux.Handle("POST /api/connections", s.requireAdmin(http.HandlerFunc(s.createConnection)))
	mux.Handle("PUT /api/connections/{id}", s.requireAdmin(http.HandlerFunc(s.updateConnection)))
	mux.Handle("DELETE /api/connections/{id}", s.requireAdmin(http.HandlerFunc(s.deleteConnection)))
	mux.Handle("POST /api/connections/{id}/test", s.requireAdmin(http.HandlerFunc(s.testConnection)))
	mux.Handle("POST /api/connections/ssh/host-key", s.requireAdmin(http.HandlerFunc(s.discoverSSHHostKey)))
	mux.Handle("GET /api/connections/{id}/schema", s.authenticated(http.HandlerFunc(s.connectionSchema)))
	mux.Handle("POST /api/connections/{id}/schema/refresh", s.authenticated(http.HandlerFunc(s.refreshConnectionSchema)))
	mux.Handle("GET /api/connections/{id}/ddl", s.authenticated(http.HandlerFunc(s.objectDDL)))
	mux.Handle("GET /api/connections/{id}/databases", s.authenticated(http.HandlerFunc(s.listDatabases)))
	mux.Handle("POST /api/connections/{id}/documents/find", s.authenticated(http.HandlerFunc(s.mongoFind)))
	mux.Handle("POST /api/connections/{id}/documents/aggregate", s.authenticated(http.HandlerFunc(s.mongoAggregate)))
	mux.Handle("POST /api/connections/{id}/documents/insert", s.authenticated(http.HandlerFunc(s.mongoInsert)))
	mux.Handle("POST /api/connections/{id}/documents/insertMany", s.authenticated(http.HandlerFunc(s.mongoInsertMany)))
	mux.Handle("POST /api/connections/{id}/documents/update", s.authenticated(http.HandlerFunc(s.mongoUpdate)))
	mux.Handle("POST /api/connections/{id}/documents/delete", s.authenticated(http.HandlerFunc(s.mongoDelete)))
	mux.Handle("POST /api/connections/{id}/documents/txn/begin", s.authenticated(http.HandlerFunc(s.mongoBeginTransaction)))
	mux.Handle("POST /api/connections/{id}/documents/txn/{txn_id}/insert", s.authenticated(http.HandlerFunc(s.mongoTxnInsert)))
	mux.Handle("POST /api/connections/{id}/documents/txn/{txn_id}/update", s.authenticated(http.HandlerFunc(s.mongoTxnUpdate)))
	mux.Handle("POST /api/connections/{id}/documents/txn/{txn_id}/delete", s.authenticated(http.HandlerFunc(s.mongoTxnDelete)))
	mux.Handle("POST /api/connections/{id}/documents/txn/{txn_id}/commit", s.authenticated(http.HandlerFunc(s.mongoCommitTransaction)))
	mux.Handle("POST /api/connections/{id}/documents/txn/{txn_id}/rollback", s.authenticated(http.HandlerFunc(s.mongoRollbackTransaction)))
	mux.Handle("POST /api/connections/{id}/redis/scan", s.authenticated(http.HandlerFunc(s.redisScan)))
	mux.Handle("POST /api/connections/{id}/redis/write", s.authenticated(http.HandlerFunc(s.redisWrite)))
	mux.Handle("POST /api/connections/{id}/redis/bulkWrite", s.authenticated(http.HandlerFunc(s.redisBulkWrite)))
	mux.Handle("POST /api/connections/{id}/redis/delete", s.authenticated(http.HandlerFunc(s.redisDelete)))
	mux.Handle("POST /api/connections/{id}/cassandra/query", s.authenticated(http.HandlerFunc(s.cassandraQuery)))
	mux.Handle("POST /api/connections/{id}/elasticsearch/search", s.authenticated(http.HandlerFunc(s.elasticsearchSearch)))
	mux.Handle("POST /api/connections/{id}/elasticsearch/index", s.authenticated(http.HandlerFunc(s.elasticsearchIndex)))
	mux.Handle("POST /api/connections/{id}/elasticsearch/bulkIndex", s.authenticated(http.HandlerFunc(s.elasticsearchBulkIndex)))
	mux.Handle("POST /api/connections/{id}/elasticsearch/update", s.authenticated(http.HandlerFunc(s.elasticsearchUpdate)))
	mux.Handle("POST /api/connections/{id}/elasticsearch/delete", s.authenticated(http.HandlerFunc(s.elasticsearchDelete)))
	mux.Handle("POST /api/connections/{id}/query", s.authenticated(http.HandlerFunc(s.runQuery)))
	mux.Handle("POST /api/multirun", s.authenticated(http.HandlerFunc(s.runOnConnections)))
	mux.Handle("POST /api/connections/{id}/explain", s.authenticated(http.HandlerFunc(s.explainQuery)))
	mux.Handle("POST /api/connections/{id}/export", s.authenticated(http.HandlerFunc(s.exportTable)))
	mux.Handle("POST /api/connections/{id}/imports", s.authenticated(http.HandlerFunc(s.startImport)))
	mux.Handle("PUT /api/connections/{id}/imports/{importId}", s.authenticated(http.HandlerFunc(s.appendImport)))
	mux.Handle("POST /api/connections/{id}/imports/{importId}/run", s.authenticated(http.HandlerFunc(s.runImport)))
	mux.Handle("DELETE /api/connections/{id}/imports/{importId}", s.authenticated(http.HandlerFunc(s.discardImport)))
	mux.Handle("POST /api/connections/{id}/txn/begin", s.authenticated(http.HandlerFunc(s.beginTransaction)))
	mux.Handle("POST /api/connections/{id}/txn/{txn_id}/query", s.authenticated(http.HandlerFunc(s.transactionQuery)))
	mux.Handle("POST /api/connections/{id}/txn/{txn_id}/commit", s.authenticated(http.HandlerFunc(s.commitTransaction)))
	mux.Handle("POST /api/connections/{id}/txn/{txn_id}/rollback", s.authenticated(http.HandlerFunc(s.rollbackTransaction)))
	mux.Handle("GET /api/connections/{id}/history", s.authenticated(http.HandlerFunc(s.queryHistory)))
	mux.Handle("GET /api/history", s.authenticated(http.HandlerFunc(s.myHistory)))
	mux.Handle("GET /api/saved-queries", s.authenticated(http.HandlerFunc(s.listSavedQueries)))
	mux.Handle("GET /api/workspace", s.authenticated(http.HandlerFunc(s.getWorkspace)))
	mux.Handle("PUT /api/workspace", s.authenticated(http.HandlerFunc(s.putWorkspace)))
	mux.Handle("GET /api/notebooks", s.authenticated(http.HandlerFunc(s.listNotebooks)))
	mux.Handle("POST /api/notebooks", s.authenticated(http.HandlerFunc(s.createNotebook)))
	mux.Handle("GET /api/notebooks/{id}", s.authenticated(http.HandlerFunc(s.getNotebook)))
	mux.Handle("PUT /api/notebooks/{id}", s.authenticated(http.HandlerFunc(s.putNotebook)))
	mux.Handle("DELETE /api/notebooks/{id}", s.authenticated(http.HandlerFunc(s.deleteNotebook)))
	if !s.config.Shared {
		mux.Handle("GET /api/scheduled-queries", s.authenticated(http.HandlerFunc(s.listScheduled)))
		mux.Handle("GET /api/scheduled-queries/defaults", s.authenticated(http.HandlerFunc(s.scheduleDefaults)))
		mux.Handle("POST /api/scheduled-queries", s.authenticated(http.HandlerFunc(s.createScheduled)))
		mux.Handle("PUT /api/scheduled-queries/{id}", s.authenticated(http.HandlerFunc(s.updateScheduled)))
		mux.Handle("DELETE /api/scheduled-queries/{id}", s.authenticated(http.HandlerFunc(s.deleteScheduled)))
		mux.Handle("POST /api/scheduled-queries/{id}/run", s.authenticated(http.HandlerFunc(s.runScheduledNow)))
		mux.Handle("GET /api/scheduled-queries/{id}/runs", s.authenticated(http.HandlerFunc(s.listScheduledRuns)))
		mux.Handle("GET /api/slack/settings", s.authenticated(http.HandlerFunc(s.slackSettings)))
		mux.Handle("PUT /api/slack/settings", s.authenticated(http.HandlerFunc(s.saveSlackSettings)))
		mux.Handle("POST /api/slack/test", s.authenticated(http.HandlerFunc(s.testSlack)))
		mux.Handle("GET /api/ai/settings", s.authenticated(http.HandlerFunc(s.aiSettings)))
		mux.Handle("PUT /api/ai/settings", s.authenticated(http.HandlerFunc(s.saveAISettings)))
		mux.Handle("POST /api/ai/ask", s.authenticated(http.HandlerFunc(s.askAI)))
		mux.Handle("GET /api/row-backups", s.authenticated(http.HandlerFunc(s.listRowBackups)))
		mux.Handle("GET /api/row-backups/{id}/restore", s.authenticated(http.HandlerFunc(s.rowBackupRestore)))
		mux.Handle("POST /api/row-backups/{id}/apply", s.authenticated(http.HandlerFunc(s.applyRowBackup)))
		mux.Handle("DELETE /api/row-backups/{id}", s.authenticated(http.HandlerFunc(s.deleteRowBackup)))
	}
	mux.Handle("POST /api/saved-queries", s.authenticated(http.HandlerFunc(s.createSavedQuery)))
	mux.Handle("DELETE /api/saved-queries/{id}", s.authenticated(http.HandlerFunc(s.deleteSavedQuery)))
	mux.Handle("GET /api/policies", s.authenticated(http.HandlerFunc(s.listPolicies)))
	mux.Handle("PATCH /api/policies/{key}", s.requireAdmin(http.HandlerFunc(s.togglePolicy)))
	mux.Handle("DELETE /api/policies/{key}", s.requireAdmin(http.HandlerFunc(s.deletePolicyScope)))
	mux.Handle("GET /api/policies/custom", s.authenticated(http.HandlerFunc(s.listCustomPolicies)))
	mux.Handle("POST /api/policies/custom", s.requireAdmin(http.HandlerFunc(s.createCustomPolicy)))
	mux.Handle("PUT /api/policies/custom/{id}", s.requireAdmin(http.HandlerFunc(s.updateCustomPolicy)))
	mux.Handle("PATCH /api/policies/custom/{id}", s.requireAdmin(http.HandlerFunc(s.patchCustomPolicy)))
	mux.Handle("DELETE /api/policies/custom/{id}", s.requireAdmin(http.HandlerFunc(s.deleteCustomPolicy)))
	for _, register := range s.routeRegistrars {
		register(mux, Kit{server: s})
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
	})
	mux.Handle("/", web.Handler())
	return s.middleware(mux)
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "NOT_READY", "store unreachable")
		return
	}
	if err := s.activity.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "NOT_READY", "activity backend unreachable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

type contextKey struct{}

func identityFromContext(ctx context.Context) domain.Identity {
	identity, _ := ctx.Value(contextKey{}).(domain.Identity)
	return identity
}

func (s *Server) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, err := s.authenticate(r)
		if err != nil {
			// Requests without a bearer token were already charged to the public
			// IP bucket by middleware. Invalid bearer traffic reaches this point
			// through the authenticated fast path, so charge it here before the
			// comparatively expensive verification path can be abused repeatedly.
			if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") && !s.allowRequest(r) {
				writeRateLimited(w)
				return
			}
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", err.Error())
			return
		}
		if !s.allowIdentity(identity.UserID) {
			writeRateLimited(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, identity)))
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return s.authenticated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !identityFromContext(r.Context()).IsAdmin() {
			writeError(w, http.StatusForbidden, "POLICY_DENIED", "admin role required")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (s *Server) authenticate(r *http.Request) (domain.Identity, error) {
	raw := r.Header.Get("Authorization")
	if !strings.HasPrefix(raw, "Bearer ") {
		return domain.Identity{}, errors.New("missing bearer token")
	}
	tokenIdentity, tokenAuthVersion, tokenSessionVersion, err := s.issuer.Verify(strings.TrimPrefix(raw, "Bearer "))
	if err != nil {
		return domain.Identity{}, errors.New("invalid bearer token")
	}
	user, err := s.store.User(r.Context(), tokenIdentity.UserID)
	if err != nil || user.Status != "active" || user.OrgID != tokenIdentity.OrgID {
		return domain.Identity{}, errors.New("account disabled or no longer valid")
	}
	role, err := s.role(r.Context(), user.ID)
	if err != nil {
		return domain.Identity{}, errors.New("role missing")
	}
	currentAuth := auth.PasswordAuthVersion(user.PasswordHash)
	if tokenAuthVersion == nil || *tokenAuthVersion != currentAuth {
		return domain.Identity{}, errors.New("session is no longer valid")
	}
	currentSession, err := s.store.UserSessionVersion(r.Context(), user.ID)
	if err != nil || tokenSessionVersion == nil || *tokenSessionVersion != currentSession {
		return domain.Identity{}, errors.New("session is no longer valid")
	}
	return domain.Identity{UserID: user.ID, OrgID: user.OrgID, Email: user.Email, Role: role.Name}, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON request")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writePolicyError(w http.ResponseWriter, status int, code string, decision policy.Decision, details map[string]any) {
	body := map[string]any{"code": code, "message": decision.Reason, "policy": decision.PolicyID, "risk": decision.Risk}
	for key, value := range details {
		body[key] = value
	}
	writeJSON(w, status, map[string]any{"error": body})
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrDuplicate) {
		writeError(w, http.StatusConflict, "ALIAS_EXISTS", "resource already exists")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "resource not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "INTERNAL", "database error")
}
