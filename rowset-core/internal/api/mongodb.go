package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/dbaopsio/rowset-studio/rowset-core/internal/engine"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/policy"
	sqlguard "github.com/dbaopsio/rowset-studio/rowset-parser"
)

func (s *Server) mongoFind(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	identity := identityFromContext(r.Context())
	if connection.Engine != "mongodb" || s.config.Shared || !identity.IsAdmin() {
		writeError(w, 403, "UNSUPPORTED", "document queries are available to personal workspace administrators only")
		return
	}
	var input engine.MongoFindInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Collection = strings.TrimSpace(input.Collection)
	if input.Collection == "" || strings.ContainsAny(input.Collection, "\x00\r\n") {
		writeError(w, 400, "BAD_REQUEST", "collection is required")
		return
	}
	if input.Limit == 0 {
		input.Limit = 100
	}
	if input.Limit < 1 || input.Limit > 10000 {
		writeError(w, 400, "BAD_REQUEST", "limit must be between 1 and 10000")
		return
	}
	filter, err := engine.ParseMongoFilter(input.Filter)
	if err != nil {
		writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	database := input.Database
	if database == "" {
		database = connection.Database
	}
	// Represents the read for existing table/schema and SELECT policies; it
	// is never turned into SQL text or sent to a database.
	info := nosqlStatement(sqlguard.Select, database, input.Collection, mongoFilterHasPredicate(filter))
	disabled, enabled, limit, timeout, err := s.resolvePolicies(r, identity, connection)
	if err != nil {
		writeError(w, 500, "INTERNAL", "policies unavailable")
		return
	}
	decision := policy.Evaluate(policy.Input{Statement: info, Role: "admin", Environment: connection.Environment, Disabled: disabled, Enabled: enabled})
	decision, limit, err = s.applyCustomPolicies(r, identity, connection, info, false, decision, limit)
	raw, _ := json.Marshal(input)
	if err != nil {
		writeError(w, 500, "INTERNAL", "policies unavailable")
		return
	}
	if decision.Effect != policy.Allow {
		s.recordActivity(r, connection.ID, string(raw), "blocked", 0, 0, "", "", auditMeta{decision: "deny", reason: decision.Reason, policyID: decision.PolicyID})
		writePolicyError(w, 403, "POLICY_DENIED", decision, nil)
		return
	}
	if limit > 0 && limit < input.Limit {
		input.Limit = limit
	}
	target, err := s.engineConnection(r, connection, database)
	if err != nil {
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	ctx, cancel := withConnectionTimeout(r, connection, timeout, 10*time.Minute)
	defer cancel()
	started := time.Now()
	docs, truncated, err := s.engines.MongoFind(ctx, target, input)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", int64(len(docs)), duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, map[string]any{"documents": docs, "truncated": truncated, "durationMs": duration, "limit": input.Limit})
}

func (s *Server) mongoAggregate(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	identity := identityFromContext(r.Context())
	if connection.Engine != "mongodb" || s.config.Shared || !identity.IsAdmin() {
		writeError(w, 403, "UNSUPPORTED", "document queries are available to personal workspace administrators only")
		return
	}
	var input engine.MongoAggregateInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Collection = strings.TrimSpace(input.Collection)
	if input.Collection == "" || strings.ContainsAny(input.Collection, "\x00\r\n") {
		writeError(w, 400, "BAD_REQUEST", "collection is required")
		return
	}
	if input.Limit == 0 {
		input.Limit = 100
	}
	if input.Limit < 1 || input.Limit > 10000 {
		writeError(w, 400, "BAD_REQUEST", "limit must be between 1 and 10000")
		return
	}
	if _, err := engine.ValidateMongoPipeline(input.Pipeline); err != nil {
		writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	database := input.Database
	if database == "" {
		database = connection.Database
	}
	// A pipeline is always treated as scoped (HasWhere: true): its stages
	// are already explicit about what they operate on, unlike a bare find({}).
	info := nosqlStatement(sqlguard.Select, database, input.Collection, true)
	disabled, enabled, limit, timeout, err := s.resolvePolicies(r, identity, connection)
	if err != nil {
		writeError(w, 500, "INTERNAL", "policies unavailable")
		return
	}
	decision := policy.Evaluate(policy.Input{Statement: info, Role: "admin", Environment: connection.Environment, Disabled: disabled, Enabled: enabled})
	decision, limit, err = s.applyCustomPolicies(r, identity, connection, info, false, decision, limit)
	raw, _ := json.Marshal(input)
	if err != nil {
		writeError(w, 500, "INTERNAL", "policies unavailable")
		return
	}
	if decision.Effect != policy.Allow {
		s.recordActivity(r, connection.ID, string(raw), "blocked", 0, 0, "", "", auditMeta{decision: "deny", reason: decision.Reason, policyID: decision.PolicyID})
		writePolicyError(w, 403, "POLICY_DENIED", decision, nil)
		return
	}
	if limit > 0 && limit < input.Limit {
		input.Limit = limit
	}
	target, err := s.engineConnection(r, connection, database)
	if err != nil {
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	ctx, cancel := withConnectionTimeout(r, connection, timeout, 10*time.Minute)
	defer cancel()
	started := time.Now()
	docs, truncated, err := s.engines.MongoAggregate(ctx, target, input)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", int64(len(docs)), duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, map[string]any{"documents": docs, "truncated": truncated, "durationMs": duration, "limit": input.Limit})
}

func (s *Server) mongoInsert(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	identity := identityFromContext(r.Context())
	if connection.Engine != "mongodb" || s.config.Shared || !identity.IsAdmin() {
		writeError(w, 403, "UNSUPPORTED", "document writes are available to personal workspace administrators only")
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
	database := input.Database
	if database == "" {
		database = connection.Database
	}
	info := nosqlStatement(sqlguard.Insert, database, input.Collection, false)
	raw, _ := json.Marshal(input)
	target, ctx, cancel, run := s.nosqlWriteGuard(w, r, connection, database, info, string(raw))
	if !run {
		return
	}
	defer cancel()
	started := time.Now()
	id, err := s.engines.MongoInsertOne(ctx, target, input)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", 1, duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, map[string]any{"id": json.RawMessage(id), "durationMs": duration})
}

// mongoInsertMany is mongoInsert for several documents in one write (up to
// 10,000), the same "no bulk APIs" limit row backups and CSV import use.
func (s *Server) mongoInsertMany(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	identity := identityFromContext(r.Context())
	if connection.Engine != "mongodb" || s.config.Shared || !identity.IsAdmin() {
		writeError(w, 403, "UNSUPPORTED", "document writes are available to personal workspace administrators only")
		return
	}
	var input engine.MongoInsertManyInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Collection = strings.TrimSpace(input.Collection)
	if input.Collection == "" {
		writeError(w, 400, "BAD_REQUEST", "collection is required")
		return
	}
	if len(input.Documents) == 0 {
		writeError(w, 400, "BAD_REQUEST", "at least one document is required")
		return
	}
	if len(input.Documents) > 10000 {
		writeError(w, 400, "BAD_REQUEST", "at most 10000 documents can be inserted at once")
		return
	}
	database := input.Database
	if database == "" {
		database = connection.Database
	}
	info := nosqlStatement(sqlguard.Insert, database, input.Collection, false)
	raw, _ := json.Marshal(input)
	target, ctx, cancel, run := s.nosqlWriteGuard(w, r, connection, database, info, string(raw))
	if !run {
		return
	}
	defer cancel()
	started := time.Now()
	ids, err := s.engines.MongoInsertMany(ctx, target, input)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	rawIDs := make([]json.RawMessage, len(ids))
	for i, id := range ids {
		rawIDs[i] = json.RawMessage(id)
	}
	s.recordActivity(r, connection.ID, string(raw), "success", int64(len(ids)), duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, map[string]any{"ids": rawIDs, "durationMs": duration})
}

func (s *Server) mongoUpdate(w http.ResponseWriter, r *http.Request) {
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
		engine.MongoUpdateInput
		Backup bool `json:"backup"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Collection = strings.TrimSpace(input.Collection)
	if input.Collection == "" {
		writeError(w, 400, "BAD_REQUEST", "collection is required")
		return
	}
	database := input.Database
	if database == "" {
		database = connection.Database
	}
	filter, err := engine.ParseMongoFilter(input.Filter)
	if err != nil {
		writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	// hasWhere reflects the real filter, not a placeholder: an UPDATE with
	// no predicate is exactly what the "no UPDATE/DELETE without WHERE"
	// guardrail exists to catch.
	info := nosqlStatement(sqlguard.Update, database, input.Collection, mongoFilterHasPredicate(filter))
	raw, _ := json.Marshal(input)
	target, ctx, cancel, run := s.nosqlWriteGuard(w, r, connection, database, info, string(raw))
	if !run {
		return
	}
	defer cancel()
	var backupAnnotations Annotations
	if input.Backup {
		backupAnnotations = s.mongoCaptureBackup(ctx, identity, connection, target, database, input.Collection, "update", string(raw), input.Filter)
		if reason, ok := backupAnnotations["backupBlocked"].(string); ok {
			writeError(w, http.StatusConflict, "BACKUP_UNAVAILABLE", reason)
			return
		}
	}
	started := time.Now()
	matched, modified, err := s.engines.MongoUpdateOne(ctx, target, input.MongoUpdateInput)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.discardRowBackup(identity, backupAnnotations)
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", modified, duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, backupAnnotations.addTo(map[string]any{"matchedCount": matched, "modifiedCount": modified, "durationMs": duration}))
}

func (s *Server) mongoDelete(w http.ResponseWriter, r *http.Request) {
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
		engine.MongoDeleteInput
		Backup bool `json:"backup"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Collection = strings.TrimSpace(input.Collection)
	if input.Collection == "" {
		writeError(w, 400, "BAD_REQUEST", "collection is required")
		return
	}
	database := input.Database
	if database == "" {
		database = connection.Database
	}
	filter, err := engine.ParseMongoFilter(input.Filter)
	if err != nil {
		writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
	info := nosqlStatement(sqlguard.Delete, database, input.Collection, mongoFilterHasPredicate(filter))
	raw, _ := json.Marshal(input)
	target, ctx, cancel, run := s.nosqlWriteGuard(w, r, connection, database, info, string(raw))
	if !run {
		return
	}
	defer cancel()
	var backupAnnotations Annotations
	if input.Backup {
		backupAnnotations = s.mongoCaptureBackup(ctx, identity, connection, target, database, input.Collection, "delete", string(raw), input.Filter)
		if reason, ok := backupAnnotations["backupBlocked"].(string); ok {
			writeError(w, http.StatusConflict, "BACKUP_UNAVAILABLE", reason)
			return
		}
	}
	started := time.Now()
	deleted, err := s.engines.MongoDeleteOne(ctx, target, input.MongoDeleteInput)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.discardRowBackup(identity, backupAnnotations)
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", deleted, duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, backupAnnotations.addTo(map[string]any{"deletedCount": deleted, "durationMs": duration}))
}

// A literal WHERE TRUE is deliberately rejected by the SQL policy parser.
// Count field predicates and conservative logical combinations instead of
// treating every nonempty MongoDB filter (e.g. {$or:[{}]}) as restrictive.
func mongoFilterHasPredicate(filter bson.D) bool {
	for _, entry := range filter {
		if !strings.HasPrefix(entry.Key, "$") {
			return true
		}
		clauses, ok := entry.Value.(bson.A)
		if !ok || len(clauses) == 0 {
			continue
		}
		switch entry.Key {
		case "$and":
			for _, clause := range clauses {
				if doc, ok := clause.(bson.D); ok && mongoFilterHasPredicate(doc) {
					return true
				}
			}
		case "$or":
			all := true
			for _, clause := range clauses {
				doc, ok := clause.(bson.D)
				if !ok || !mongoFilterHasPredicate(doc) {
					all = false
					break
				}
			}
			if all {
				return true
			}
		}
	}
	return false
}
