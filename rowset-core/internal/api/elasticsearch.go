package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/dbaopsio/rowset-studio/rowset-core/internal/engine"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/policy"
	sqlguard "github.com/dbaopsio/rowset-studio/rowset-parser"
)

func (s *Server) elasticsearchSearch(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	identity := identityFromContext(r.Context())
	if connection.Engine != "elasticsearch" || s.config.Shared || !identity.IsAdmin() {
		writeError(w, 403, "UNSUPPORTED", "index search is available to personal workspace administrators only")
		return
	}
	var input engine.ElasticsearchSearchInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Index = strings.TrimSpace(input.Index)
	if input.Index == "" || strings.ContainsAny(input.Index, "\x00\r\n") {
		writeError(w, 400, "BAD_REQUEST", "index is required")
		return
	}
	if input.Size == 0 {
		input.Size = 100
	}
	if input.Size < 1 || input.Size > 10000 {
		writeError(w, 400, "BAD_REQUEST", "size must be between 1 and 10000")
		return
	}
	quote := func(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
	database := connection.Database
	if database == "" {
		database = "elasticsearch"
	}
	statement := "SELECT * FROM " + quote(database) + "." + quote(input.Index)
	hasQuery := len(input.Query) > 0 && strings.TrimSpace(string(input.Query)) != "{}" || len(input.SearchAfter) > 0 || len(input.Aggs) > 0
	if hasQuery {
		statement += ` WHERE "__rowset_search_query__" IS NOT NULL`
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
	if limit > 0 && limit < input.Size {
		input.Size = limit
	}
	target, err := s.engineConnection(r, connection, database)
	if err != nil {
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	ctx, cancel := withConnectionTimeout(r, connection, timeout, 10*time.Minute)
	defer cancel()
	started := time.Now()
	result, err := s.engines.ElasticsearchSearch(ctx, target, input)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", int64(len(result.Documents)), duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, map[string]any{"documents": result.Documents, "truncated": result.Truncated, "aggregations": result.Aggregations, "searchAfter": result.SearchAfter, "durationMs": duration, "limit": input.Size})
}

func (s *Server) elasticsearchIndex(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	identity := identityFromContext(r.Context())
	if connection.Engine != "elasticsearch" || s.config.Shared || !identity.IsAdmin() {
		writeError(w, 403, "UNSUPPORTED", "document writes are available to personal workspace administrators only")
		return
	}
	var input engine.ElasticsearchIndexInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Index, input.ID = strings.TrimSpace(input.Index), strings.TrimSpace(input.ID)
	if input.Index == "" || input.ID == "" {
		writeError(w, 400, "BAD_REQUEST", "index and id are required")
		return
	}
	database := connection.Database
	if database == "" {
		database = "elasticsearch"
	}
	quote := func(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
	statement := "UPDATE " + quote(database) + "." + quote(input.Index) + ` SET "document" = 'rowset' WHERE "__rowset_document_id__" IS NOT NULL`
	raw, _ := json.Marshal(input)
	target, ctx, cancel, run := s.nosqlWriteGuard(w, r, connection, database, statement, string(raw))
	if !run {
		return
	}
	defer cancel()
	started := time.Now()
	id, err := s.engines.ElasticsearchIndex(ctx, target, input)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", 1, duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, map[string]any{"id": id, "durationMs": duration})
}

func (s *Server) elasticsearchUpdate(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	identity := identityFromContext(r.Context())
	if connection.Engine != "elasticsearch" || s.config.Shared || !identity.IsAdmin() {
		writeError(w, 403, "UNSUPPORTED", "document writes are available to personal workspace administrators only")
		return
	}
	var input engine.ElasticsearchUpdateInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Index, input.ID = strings.TrimSpace(input.Index), strings.TrimSpace(input.ID)
	if input.Index == "" || input.ID == "" {
		writeError(w, 400, "BAD_REQUEST", "index and id are required")
		return
	}
	database := connection.Database
	if database == "" {
		database = "elasticsearch"
	}
	quote := func(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
	statement := "UPDATE " + quote(database) + "." + quote(input.Index) + ` SET "document" = 'rowset' WHERE "__rowset_document_id__" IS NOT NULL`
	raw, _ := json.Marshal(input)
	target, ctx, cancel, run := s.nosqlWriteGuard(w, r, connection, database, statement, string(raw))
	if !run {
		return
	}
	defer cancel()
	started := time.Now()
	err := s.engines.ElasticsearchUpdate(ctx, target, input)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", 1, duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, map[string]any{"durationMs": duration})
}

func (s *Server) elasticsearchDelete(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	identity := identityFromContext(r.Context())
	if connection.Engine != "elasticsearch" || s.config.Shared || !identity.IsAdmin() {
		writeError(w, 403, "UNSUPPORTED", "document writes are available to personal workspace administrators only")
		return
	}
	var input engine.ElasticsearchDeleteInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Index, input.ID = strings.TrimSpace(input.Index), strings.TrimSpace(input.ID)
	if input.Index == "" || input.ID == "" {
		writeError(w, 400, "BAD_REQUEST", "index and id are required")
		return
	}
	database := connection.Database
	if database == "" {
		database = "elasticsearch"
	}
	quote := func(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
	statement := "DELETE FROM " + quote(database) + "." + quote(input.Index) + ` WHERE "__rowset_document_id__" IS NOT NULL`
	raw, _ := json.Marshal(input)
	target, ctx, cancel, run := s.nosqlWriteGuard(w, r, connection, database, statement, string(raw))
	if !run {
		return
	}
	defer cancel()
	started := time.Now()
	err := s.engines.ElasticsearchDelete(ctx, target, input)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", 1, duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, map[string]any{"durationMs": duration})
}
