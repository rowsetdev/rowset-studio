package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/policy"
	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
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
	database := connection.Database
	if database == "" {
		database = "elasticsearch"
	}
	hasQuery := len(input.Query) > 0 && strings.TrimSpace(string(input.Query)) != "{}" || len(input.SearchAfter) > 0 || len(input.Aggs) > 0
	info := nosqlStatement(sqlguard.Select, database, input.Index, hasQuery)
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
	// Always targets exactly one document by id, index or replace.
	info := nosqlStatement(sqlguard.Update, database, input.Index, true)
	raw, _ := json.Marshal(input)
	target, ctx, cancel, run := s.nosqlWriteGuard(w, r, connection, database, info, string(raw))
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

// elasticsearchBulkIndex is elasticsearchIndex for several documents in one
// _bulk request (up to 10,000), the same "no bulk APIs" limit row backups
// and CSV import use.
func (s *Server) elasticsearchBulkIndex(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	identity := identityFromContext(r.Context())
	if connection.Engine != "elasticsearch" || s.config.Shared || !identity.IsAdmin() {
		writeError(w, 403, "UNSUPPORTED", "document writes are available to personal workspace administrators only")
		return
	}
	var input engine.ElasticsearchBulkIndexInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Index = strings.TrimSpace(input.Index)
	if input.Index == "" {
		writeError(w, 400, "BAD_REQUEST", "index is required")
		return
	}
	if len(input.Documents) == 0 {
		writeError(w, 400, "BAD_REQUEST", "at least one document is required")
		return
	}
	if len(input.Documents) > 10000 {
		writeError(w, 400, "BAD_REQUEST", "at most 10000 documents can be indexed at once")
		return
	}
	database := connection.Database
	if database == "" {
		database = "elasticsearch"
	}
	info := nosqlStatement(sqlguard.Insert, database, input.Index, false)
	raw, _ := json.Marshal(input)
	target, ctx, cancel, run := s.nosqlWriteGuard(w, r, connection, database, info, string(raw))
	if !run {
		return
	}
	defer cancel()
	started := time.Now()
	result, err := s.engines.ElasticsearchBulkIndex(ctx, target, input)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", int64(len(result.IDs)), duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, map[string]any{"ids": result.IDs, "errors": result.Errors, "durationMs": duration})
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
	var input struct {
		engine.ElasticsearchUpdateInput
		Backup bool `json:"backup"`
	}
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
	// Always targets exactly one document by id.
	info := nosqlStatement(sqlguard.Update, database, input.Index, true)
	raw, _ := json.Marshal(input)
	target, ctx, cancel, run := s.nosqlWriteGuard(w, r, connection, database, info, string(raw))
	if !run {
		return
	}
	defer cancel()
	var backupAnnotations Annotations
	if input.Backup {
		backupAnnotations = s.elasticsearchCaptureBackup(ctx, identity, connection, target, database, input.Index, input.ID, "update", string(raw))
	}
	started := time.Now()
	err := s.engines.ElasticsearchUpdate(ctx, target, input.ElasticsearchUpdateInput)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.discardRowBackup(identity, backupAnnotations)
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", 1, duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, backupAnnotations.addTo(map[string]any{"durationMs": duration}))
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
	var input struct {
		engine.ElasticsearchDeleteInput
		Backup bool `json:"backup"`
	}
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
	// Always targets exactly one document by id.
	info := nosqlStatement(sqlguard.Delete, database, input.Index, true)
	raw, _ := json.Marshal(input)
	target, ctx, cancel, run := s.nosqlWriteGuard(w, r, connection, database, info, string(raw))
	if !run {
		return
	}
	defer cancel()
	var backupAnnotations Annotations
	if input.Backup {
		backupAnnotations = s.elasticsearchCaptureBackup(ctx, identity, connection, target, database, input.Index, input.ID, "delete", string(raw))
	}
	started := time.Now()
	err := s.engines.ElasticsearchDelete(ctx, target, input.ElasticsearchDeleteInput)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.discardRowBackup(identity, backupAnnotations)
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", 1, duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, backupAnnotations.addTo(map[string]any{"durationMs": duration}))
}
