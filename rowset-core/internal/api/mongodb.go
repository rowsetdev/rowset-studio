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
	// Represent the read for existing table/schema and SELECT policies. The
	// synthetic SQL is never sent to a database; only the validated find runs.
	database := input.Database
	if database == "" {
		database = connection.Database
	}
	quote := func(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
	statement := "SELECT * FROM " + quote(database) + "." + quote(input.Collection)
	if mongoFilterHasPredicate(filter) {
		statement += ` WHERE "__rowset_document_filter__" IS NOT NULL`
	}
	info, err := sqlguard.ParseDialect(sqlguard.DialectPostgres, statement)
	if err != nil {
		writeError(w, 400, "BAD_REQUEST", err.Error())
		return
	}
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
	quote := func(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
	statement := "INSERT INTO " + quote(database) + "." + quote(input.Collection) + ` ("document") VALUES ('rowset')`
	raw, _ := json.Marshal(input)
	target, ctx, cancel, run := s.nosqlWriteGuard(w, r, connection, database, statement, string(raw))
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
	var input engine.MongoUpdateInput
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
	quote := func(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
	statement := "UPDATE " + quote(database) + "." + quote(input.Collection) + ` SET "document" = 'rowset' WHERE "__rowset_document_filter__" IS NOT NULL`
	raw, _ := json.Marshal(input)
	target, ctx, cancel, run := s.nosqlWriteGuard(w, r, connection, database, statement, string(raw))
	if !run {
		return
	}
	defer cancel()
	started := time.Now()
	matched, modified, err := s.engines.MongoUpdateOne(ctx, target, input)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", modified, duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, map[string]any{"matchedCount": matched, "modifiedCount": modified, "durationMs": duration})
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
	var input engine.MongoDeleteInput
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
	quote := func(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
	statement := "DELETE FROM " + quote(database) + "." + quote(input.Collection) + ` WHERE "__rowset_document_filter__" IS NOT NULL`
	raw, _ := json.Marshal(input)
	target, ctx, cancel, run := s.nosqlWriteGuard(w, r, connection, database, statement, string(raw))
	if !run {
		return
	}
	defer cancel()
	started := time.Now()
	deleted, err := s.engines.MongoDeleteOne(ctx, target, input)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", deleted, duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, map[string]any{"deletedCount": deleted, "durationMs": duration})
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
