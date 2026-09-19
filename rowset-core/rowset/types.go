package rowset

import (
	"context"
	"net/http"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/activity"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/api"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/config"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/id"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/policy"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

// Domain types.
type (
	Identity             = domain.Identity
	Connection           = domain.Connection
	ConnectionNode       = domain.ConnectionNode
	ColumnOrigin         = domain.ColumnOrigin
	User                 = domain.User
	Role                 = domain.Role
	Organization         = domain.Organization
	RoleConnectionAccess = domain.RoleConnectionAccess
	Secret               = domain.Secret
	AuditLog             = domain.AuditLog
	QueryHistory         = domain.QueryHistory
	Decision             = policy.Decision
	Effect               = policy.Effect
	Risk                 = policy.Risk
	CustomPolicy         = domain.CustomPolicy
	Config               = config.Config
)

// Extension points of a server; see Server's Add* and Set* methods.
type (
	StatementRequest   = api.StatementRequest
	StatementHook      = api.StatementHook
	ResultRequest      = api.ResultRequest
	ResultTransforms   = api.ResultTransforms
	ResultHook         = api.ResultHook
	Annotations        = api.Annotations
	Escalation         = api.Escalation
	DeferredStatement  = api.DeferredStatement
	DeferredResult     = api.DeferredResult
	RouteRegistrar     = api.RouteRegistrar
	Kit                = api.Kit
	ConnectionSaveHook = api.ConnectionSaveHook
)

// Storage.
type (
	Store         = store.Store
	Storage       = store.Extension
	DefaultPolicy = store.DefaultPolicy
	ActivityStore = activity.Store
)

var (
	ErrNotFound  = store.ErrNotFound
	ErrDuplicate = store.ErrDuplicate
)

// AddStorage adds a plugin's tables and data rules to the control database.
func (s *Setup) AddStorage(storage Storage) { store.RegisterExtension(storage) }

// NewID returns a new random identifier.
func NewID() string { return id.New() }

// Now is the current time as stored in the control database.
func Now() string { return store.NowString() }

// RequestIdentity is the caller of a request that passed Kit.Authenticated or
// Kit.RequireAdmin.
func RequestIdentity(r *http.Request) Identity { return api.Identity(r) }

func WriteJSON(w http.ResponseWriter, status int, value any) { api.WriteJSON(w, status, value) }

func WriteError(w http.ResponseWriter, status int, code, message string) {
	api.WriteError(w, status, code, message)
}

// WriteStoreError maps ErrNotFound and ErrDuplicate to their HTTP responses.
func WriteStoreError(w http.ResponseWriter, err error) { api.WriteStoreError(w, err) }

// DecodeJSON decodes a request body, rejecting unknown fields; on failure it
// has already written the error response.
func DecodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	return api.DecodeJSON(w, r, target)
}

// OpenStore opens a control database, creating it or bringing its schema up
// to date, including the tables registered with AddStorage. When backupDir
// is not empty, a copy of an existing database is written there before a
// migration changes it.
func OpenStore(ctx context.Context, path, backupDir string) (*Store, error) {
	if backupDir == "" {
		return store.Open(ctx, path)
	}
	return store.Open(ctx, path, store.WithMigrationBackup(backupDir))
}

// Policies.
type (
	PolicyDefinition = api.PolicyDefinition
	PolicyRule       = policy.Rule
	PolicyInput      = policy.Input
	CustomPolicyKind = api.CustomPolicyKind
)

// Effects and risks of a policy decision. A plugin may use its own effect,
// such as one that holds a statement for review; any effect other than
// Allow and Deny is handed to the escalation workflow.
const (
	Allow        = policy.Allow
	Deny         = policy.Deny
	LowRisk      = policy.Low
	MediumRisk   = policy.Medium
	HighRisk     = policy.High
	CriticalRisk = policy.Critical
)

// AddPolicy adds a toggle policy to shared installations; rule, when not
// nil, evaluates it after the built-in guardrails (see PolicyInput.Enabled).
func (s *Setup) AddPolicy(definition PolicyDefinition, rule PolicyRule) {
	api.RegisterPolicy(definition)
	if rule != nil {
		policy.AddRule(rule)
	}
}

// AddCustomPolicyKind adds a kind of custom policy to shared installations.
func (s *Setup) AddCustomPolicyKind(kind CustomPolicyKind) { api.RegisterCustomPolicyKind(kind) }

// BatchWriter is implemented by an activity backend that stores many records
// in one write.
type BatchWriter = activity.BatchWriter

// BufferedActivity takes activity off the request path: records are queued
// (up to capacity) and handed to backend in batches by one writer.
func BufferedActivity(backend ActivityStore, capacity int) ActivityStore {
	return activity.NewBuffered(backend, capacity)
}

// LoadConfig reads the configuration file named by ROWSET_CONFIG (or
// rowset.env) into the environment, then the settings Rowset itself uses.
// It does not validate them; see Config.Validate.
func LoadConfig(defaultDBPath string) (Config, error) {
	return config.LoadEnvironment(defaultDBPath)
}

// ChainBreak names the first audit entry of an organization whose hash no
// longer matches.
type ChainBreak = store.ChainBreak

// PrepareAudit links an audit entry to the previous entry of its
// organization's hash chain; an activity backend calls it before storing
// the entry.
func PrepareAudit(previous string, item AuditLog) AuditLog { return store.PrepareAudit(previous, item) }

// VerifyPreparedAudit reports whether a stored entry still matches the hash
// PrepareAudit gave it.
func VerifyPreparedAudit(version int64, previous, expected string, item AuditLog) bool {
	return store.VerifyPreparedAudit(version, previous, expected, item)
}
