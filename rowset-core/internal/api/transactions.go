package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/id"
)

// transactionIdleTimeout is how long a manual transaction may sit unused
// before it is rolled back and its locks released. A personal workspace
// allows a longer break than a shared server.
func (s *Server) transactionIdleTimeout() time.Duration {
	if s.config.Shared {
		return 5 * time.Minute
	}
	return time.Hour
}

const errTxnRolledBack = "Commit applied nothing: the database aborted this transaction after an earlier error and rolled back all of its changes."

type transactionEntry struct {
	mu                   sync.Mutex
	transaction          *engine.Transaction
	userID, connectionID string
	lastUsed             time.Time
	// running counts statements in progress; a transaction is never idle
	// while one runs, however long it takes.
	running int
}
type beginTransactionInput struct {
	Database string `json:"database"`
}

func (s *Server) beginTransaction(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	var input beginTransactionInput
	if !decodeJSON(w, r, &input) {
		return
	}
	identity := identityFromContext(r.Context())
	desired, err := s.desiredNodeRole(r.Context(), identity, connection.ID, nil)
	if err != nil {
		writeError(w, 403, "POLICY_DENIED", err.Error())
		return
	}
	if desired == "secondary" {
		writeError(w, 403, "POLICY_DENIED", errSecondaryTxn.Error())
		return
	}
	s.reapIdleTransactions()
	s.txnMu.Lock()
	userCount, connectionCount := 0, 0
	for _, item := range s.txns {
		if item.userID == identity.UserID {
			userCount++
		}
		if item.connectionID == connection.ID {
			connectionCount++
		}
	}
	s.txnMu.Unlock()
	perUser := 5
	if !s.config.Shared {
		perUser = 20 // one per editor tab in manual commit mode
	}
	if userCount >= perUser || connectionCount >= 50 {
		writeError(w, 429, "TXN_LIMIT", fmt.Sprintf("You already have %d open manual-commit transactions. Commit or roll back one in another tab first.", userCount))
		return
	}
	node, err := s.routeNode(r.Context(), connection, "primary")
	if err != nil {
		writeError(w, 502, "TXN_ERROR", err.Error())
		return
	}
	target, err := s.engineConnectionAt(r.Context(), connection, input.Database, node.Host, node.Port)
	if err != nil {
		writeError(w, 502, "TXN_ERROR", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	transaction, err := s.engines.Begin(ctx, target)
	if err != nil {
		writeError(w, 502, "TXN_ERROR", err.Error())
		return
	}
	txnID := id.New()
	s.txnMu.Lock()
	s.txns[txnID] = &transactionEntry{transaction: transaction, userID: identity.UserID, connectionID: connection.ID, lastUsed: time.Now()}
	s.txnMu.Unlock()
	writeJSON(w, 200, map[string]string{"txnId": txnID})
}

func (s *Server) transactionEntry(w http.ResponseWriter, r *http.Request, connection domain.Connection) (*transactionEntry, bool) {
	identity := identityFromContext(r.Context())
	txnID := r.PathValue("txn_id")
	s.txnMu.Lock()
	item := s.txns[txnID]
	authorized := item != nil && item.userID == identity.UserID && item.connectionID == connection.ID
	expired := authorized && item.running == 0 && time.Since(item.lastUsed) > s.transactionIdleTimeout()
	if expired {
		delete(s.txns, txnID)
	} else if authorized {
		item.lastUsed = time.Now()
	}
	s.txnMu.Unlock()
	if expired {
		item.mu.Lock()
		_ = item.transaction.Rollback()
		item.mu.Unlock()
	}
	if !authorized || expired {
		writeError(w, 404, "TXN_NOT_FOUND", "transaction not found or expired")
		return nil, false
	}
	return item, true
}

func (s *Server) transactionQuery(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	item, ok := s.transactionEntry(w, r, connection)
	if !ok {
		return
	}
	var input queryInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.SQL = strings.TrimSpace(input.SQL)
	if input.SQL == "" {
		writeError(w, 400, "BAD_REQUEST", "sql is required")
		return
	}
	s.txnMu.Lock()
	item.running++
	s.txnMu.Unlock()
	defer func() {
		s.txnMu.Lock()
		item.running--
		item.lastUsed = time.Now()
		s.txnMu.Unlock()
	}()
	item.mu.Lock()
	defer item.mu.Unlock()
	s.executeQuery(w, r, connection, input, item.transaction)
	// Cancelled statements, server-side kills and network failures close the
	// pinned connection; the database has discarded the uncommitted work.
	if item.transaction.State() == engine.TransactionLost {
		txnID := r.PathValue("txn_id")
		s.txnMu.Lock()
		if s.txns[txnID] == item {
			delete(s.txns, txnID)
		}
		s.txnMu.Unlock()
		_ = item.transaction.Rollback()
	}
}
func (s *Server) commitTransaction(w http.ResponseWriter, r *http.Request) {
	s.finishTransaction(w, r, true)
}
func (s *Server) rollbackTransaction(w http.ResponseWriter, r *http.Request) {
	s.finishTransaction(w, r, false)
}
func (s *Server) finishTransaction(w http.ResponseWriter, r *http.Request, commit bool) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	item, ok := s.transactionEntry(w, r, connection)
	if !ok {
		return
	}
	item.mu.Lock()
	defer item.mu.Unlock()
	s.txnMu.Lock()
	delete(s.txns, r.PathValue("txn_id"))
	s.txnMu.Unlock()
	switch state := item.transaction.State(); {
	case state == engine.TransactionLost:
		_ = item.transaction.Rollback()
		if commit {
			writeError(w, http.StatusConflict, "TXN_LOST", "Commit was not sent: the transaction's database connection had already ended, so the database discarded its uncommitted changes.")
			return
		}
		w.WriteHeader(204)
		return
	case state == engine.TransactionAborted && commit:
		_ = item.transaction.Rollback()
		writeError(w, http.StatusConflict, "TXN_ROLLED_BACK", errTxnRolledBack)
		return
	}
	var err error
	if commit {
		err = item.transaction.Commit()
	} else {
		err = item.transaction.Rollback()
	}
	if errors.Is(err, pgx.ErrTxCommitRollback) {
		writeError(w, http.StatusConflict, "TXN_ROLLED_BACK", errTxnRolledBack)
		return
	}
	if err != nil {
		writeError(w, 502, "TXN_FINISH_ERROR", "Transaction handle closed. Verify the database state before retrying writes: "+err.Error())
		return
	}
	// Objects created or dropped inside the transaction only become real, or
	// disappear, when it ends.
	s.engines.InvalidateSchema(connection.ID)
	_ = s.store.DeleteSchemaSnapshots(r.Context(), connection.ID)
	w.WriteHeader(204)
}

func (s *Server) reapIdleTransactions() {
	now := time.Now()
	var expired []*transactionEntry
	s.txnMu.Lock()
	for txnID, item := range s.txns {
		if item.running == 0 && now.Sub(item.lastUsed) > s.transactionIdleTimeout() {
			delete(s.txns, txnID)
			expired = append(expired, item)
		}
	}
	s.txnMu.Unlock()
	for _, item := range expired {
		item.mu.Lock()
		_ = item.transaction.Rollback()
		item.mu.Unlock()
	}
	s.reapIdleMongoTransactions(now)
}

func (s *Server) closeConnectionTransactions(connectionID string) {
	var closed []*transactionEntry
	s.txnMu.Lock()
	for txnID, item := range s.txns {
		if item.connectionID == connectionID {
			delete(s.txns, txnID)
			closed = append(closed, item)
		}
	}
	s.txnMu.Unlock()
	for _, item := range closed {
		item.mu.Lock()
		_ = item.transaction.Rollback()
		item.mu.Unlock()
	}
	s.closeConnectionMongoTransactions(connectionID)
}
func (s *Server) transactionReaper(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reapIdleTransactions()
		}
	}
}
