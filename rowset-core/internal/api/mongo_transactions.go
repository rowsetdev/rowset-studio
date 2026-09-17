package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dbaopsio/rowset-studio/rowset-core/internal/domain"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/engine"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/id"
	sqlguard "github.com/dbaopsio/rowset-studio/rowset-parser"
)

// mongoTxnEntry pins one MongoDB session/transaction across several
// document writes, the same shape transactionEntry gives SQL engines.
// MongoDB only accepts a transaction number on a replica set member or
// mongos; a standalone mongod fails the first write inside it with a clear
// driver error, surfaced to the caller as-is.
type mongoTxnEntry struct {
	mu                   sync.Mutex
	transaction          *engine.MongoTransaction
	userID, connectionID string
	lastUsed             time.Time
	running              int
}

func (s *Server) mongoBeginTransaction(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	identity := identityFromContext(r.Context())
	if connection.Engine != "mongodb" || s.config.Shared || !identity.IsAdmin() {
		writeError(w, 403, "UNSUPPORTED", "document writes are available to personal workspace administrators only")
		return
	}
	var input struct {
		Database string `json:"database"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	s.reapIdleTransactions()
	s.mongoTxnMu.Lock()
	userCount, connectionCount := 0, 0
	for _, item := range s.mongoTxns {
		if item.userID == identity.UserID {
			userCount++
		}
		if item.connectionID == connection.ID {
			connectionCount++
		}
	}
	s.mongoTxnMu.Unlock()
	perUser := 5
	if !s.config.Shared {
		perUser = 20
	}
	if userCount >= perUser || connectionCount >= 50 {
		writeError(w, 429, "TXN_LIMIT", "You already have several open manual-commit transactions. Commit or roll back one first.")
		return
	}
	target, err := s.engineConnection(r, connection, input.Database)
	if err != nil {
		writeError(w, 502, "TXN_ERROR", err.Error())
		return
	}
	ctx, cancel := withConnectionTimeout(r, connection, 0, 10*time.Second)
	defer cancel()
	transaction, err := s.engines.MongoBegin(ctx, target)
	if err != nil {
		// Most commonly: "Transaction numbers are only allowed on a replica
		// set member or mongos" for a standalone server. That message
		// already explains the fix, so it is passed through unchanged.
		writeError(w, 502, "TXN_ERROR", err.Error())
		return
	}
	txnID := id.New()
	s.mongoTxnMu.Lock()
	s.mongoTxns[txnID] = &mongoTxnEntry{transaction: transaction, userID: identity.UserID, connectionID: connection.ID, lastUsed: time.Now()}
	s.mongoTxnMu.Unlock()
	writeJSON(w, 200, map[string]string{"txnId": txnID})
}

func (s *Server) mongoTransactionEntry(w http.ResponseWriter, r *http.Request, connection domain.Connection) (*mongoTxnEntry, bool) {
	identity := identityFromContext(r.Context())
	txnID := r.PathValue("txn_id")
	s.mongoTxnMu.Lock()
	item := s.mongoTxns[txnID]
	authorized := item != nil && item.userID == identity.UserID && item.connectionID == connection.ID
	if authorized {
		item.lastUsed = time.Now()
	}
	s.mongoTxnMu.Unlock()
	if !authorized {
		writeError(w, 404, "TXN_NOT_FOUND", "transaction not found or expired")
		return nil, false
	}
	return item, true
}

func (s *Server) mongoTxnInsert(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	item, ok := s.mongoTransactionEntry(w, r, connection)
	if !ok {
		return
	}
	var input engine.MongoInsertInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Collection = strings.TrimSpace(input.Collection)
	if input.Collection == "" {
		writeError(w, 400, "BAD_REQUEST", "collection is required")
		return
	}
	info := nosqlStatement(sqlguard.Insert, connection.Database, input.Collection, false)
	raw, _ := json.Marshal(input)
	if _, allowed := s.nosqlPolicyAllowed(w, r, connection, info, string(raw)); !allowed {
		return
	}
	s.mongoTxnMu.Lock()
	item.running++
	s.mongoTxnMu.Unlock()
	defer func() {
		s.mongoTxnMu.Lock()
		item.running--
		item.lastUsed = time.Now()
		s.mongoTxnMu.Unlock()
	}()
	item.mu.Lock()
	defer item.mu.Unlock()
	started := time.Now()
	docID, err := item.transaction.InsertOne(input)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", 1, duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, map[string]any{"id": json.RawMessage(docID), "durationMs": duration})
}

func (s *Server) mongoTxnUpdate(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	item, ok := s.mongoTransactionEntry(w, r, connection)
	if !ok {
		return
	}
	var input engine.MongoUpdateInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Collection = strings.TrimSpace(input.Collection)
	if input.Collection == "" {
		writeError(w, 400, "BAD_REQUEST", "collection is required")
		return
	}
	filter, err := engine.ParseMongoFilter(input.Filter)
	if err != nil {
		writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	info := nosqlStatement(sqlguard.Update, connection.Database, input.Collection, mongoFilterHasPredicate(filter))
	raw, _ := json.Marshal(input)
	if _, allowed := s.nosqlPolicyAllowed(w, r, connection, info, string(raw)); !allowed {
		return
	}
	s.mongoTxnMu.Lock()
	item.running++
	s.mongoTxnMu.Unlock()
	defer func() {
		s.mongoTxnMu.Lock()
		item.running--
		item.lastUsed = time.Now()
		s.mongoTxnMu.Unlock()
	}()
	item.mu.Lock()
	defer item.mu.Unlock()
	started := time.Now()
	matched, modified, err := item.transaction.UpdateOne(input)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", modified, duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, map[string]any{"matchedCount": matched, "modifiedCount": modified, "durationMs": duration})
}

func (s *Server) mongoTxnDelete(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	item, ok := s.mongoTransactionEntry(w, r, connection)
	if !ok {
		return
	}
	var input engine.MongoDeleteInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Collection = strings.TrimSpace(input.Collection)
	if input.Collection == "" {
		writeError(w, 400, "BAD_REQUEST", "collection is required")
		return
	}
	filter, err := engine.ParseMongoFilter(input.Filter)
	if err != nil {
		writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	info := nosqlStatement(sqlguard.Delete, connection.Database, input.Collection, mongoFilterHasPredicate(filter))
	raw, _ := json.Marshal(input)
	if _, allowed := s.nosqlPolicyAllowed(w, r, connection, info, string(raw)); !allowed {
		return
	}
	s.mongoTxnMu.Lock()
	item.running++
	s.mongoTxnMu.Unlock()
	defer func() {
		s.mongoTxnMu.Lock()
		item.running--
		item.lastUsed = time.Now()
		s.mongoTxnMu.Unlock()
	}()
	item.mu.Lock()
	defer item.mu.Unlock()
	started := time.Now()
	deleted, err := item.transaction.DeleteOne(input)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", deleted, duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, map[string]any{"deletedCount": deleted, "durationMs": duration})
}

func (s *Server) mongoCommitTransaction(w http.ResponseWriter, r *http.Request) {
	s.finishMongoTransaction(w, r, true)
}
func (s *Server) mongoRollbackTransaction(w http.ResponseWriter, r *http.Request) {
	s.finishMongoTransaction(w, r, false)
}
func (s *Server) finishMongoTransaction(w http.ResponseWriter, r *http.Request, commit bool) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	item, ok := s.mongoTransactionEntry(w, r, connection)
	if !ok {
		return
	}
	item.mu.Lock()
	defer item.mu.Unlock()
	s.mongoTxnMu.Lock()
	delete(s.mongoTxns, r.PathValue("txn_id"))
	s.mongoTxnMu.Unlock()
	var err error
	if commit {
		err = item.transaction.Commit()
	} else {
		err = item.transaction.Rollback()
	}
	if err != nil {
		writeError(w, 502, "TXN_FINISH_ERROR", "Transaction handle closed. Verify the database state before retrying writes: "+err.Error())
		return
	}
	w.WriteHeader(204)
}

func (s *Server) reapIdleMongoTransactions(now time.Time) {
	var expired []*mongoTxnEntry
	s.mongoTxnMu.Lock()
	for txnID, item := range s.mongoTxns {
		if item.running == 0 && now.Sub(item.lastUsed) > s.transactionIdleTimeout() {
			delete(s.mongoTxns, txnID)
			expired = append(expired, item)
		}
	}
	s.mongoTxnMu.Unlock()
	for _, item := range expired {
		item.mu.Lock()
		_ = item.transaction.Rollback()
		item.mu.Unlock()
	}
}

func (s *Server) closeConnectionMongoTransactions(connectionID string) {
	var closed []*mongoTxnEntry
	s.mongoTxnMu.Lock()
	for txnID, item := range s.mongoTxns {
		if item.connectionID == connectionID {
			delete(s.mongoTxns, txnID)
			closed = append(closed, item)
		}
	}
	s.mongoTxnMu.Unlock()
	for _, item := range closed {
		item.mu.Lock()
		_ = item.transaction.Rollback()
		item.mu.Unlock()
	}
}
