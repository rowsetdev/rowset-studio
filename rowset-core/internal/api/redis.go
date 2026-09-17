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

func (s *Server) redisScan(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	identity := identityFromContext(r.Context())
	if connection.Engine != "redis" && connection.Engine != "valkey" || s.config.Shared || !identity.IsAdmin() {
		writeError(w, 403, "UNSUPPORTED", "key browsing is available to personal workspace administrators only")
		return
	}
	var input engine.RedisScanInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Limit == 0 {
		input.Limit = 100
	}
	if input.Limit < 1 || input.Limit > 1000 {
		writeError(w, 400, "BAD_REQUEST", "limit must be between 1 and 1000")
		return
	}
	database := input.Database
	if database == "" {
		database = connection.Database
	}
	pattern := input.Pattern
	if strings.TrimSpace(pattern) == "" {
		pattern = "*"
	}
	quote := func(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
	statement := "SELECT * FROM " + quote(database) + "." + quote(pattern)
	if pattern != "*" {
		statement += ` WHERE "__rowset_key_pattern__" IS NOT NULL`
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
	entries, cursor, err := s.engines.RedisScan(ctx, target, input)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", int64(len(entries)), duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, map[string]any{"entries": entries, "cursor": cursor, "durationMs": duration, "limit": input.Limit})
}

func (s *Server) redisWrite(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	identity := identityFromContext(r.Context())
	if connection.Engine != "redis" && connection.Engine != "valkey" || s.config.Shared || !identity.IsAdmin() {
		writeError(w, 403, "UNSUPPORTED", "key writes are available to personal workspace administrators only")
		return
	}
	var input engine.RedisWriteInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Key = strings.TrimSpace(input.Key)
	if input.Key == "" {
		writeError(w, 400, "BAD_REQUEST", "key is required")
		return
	}
	database := input.Database
	if database == "" {
		database = connection.Database
	}
	quote := func(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
	statement := "UPDATE " + quote(database) + "." + quote(input.Key) + ` SET "value" = 'rowset' WHERE "__rowset_key__" IS NOT NULL`
	raw, _ := json.Marshal(input)
	target, ctx, cancel, run := s.nosqlWriteGuard(w, r, connection, database, statement, string(raw))
	if !run {
		return
	}
	defer cancel()
	started := time.Now()
	err := s.engines.RedisWrite(ctx, target, input)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", 1, duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, map[string]any{"durationMs": duration})
}

func (s *Server) redisDelete(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	identity := identityFromContext(r.Context())
	if connection.Engine != "redis" && connection.Engine != "valkey" || s.config.Shared || !identity.IsAdmin() {
		writeError(w, 403, "UNSUPPORTED", "key writes are available to personal workspace administrators only")
		return
	}
	var input engine.RedisDeleteInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Key = strings.TrimSpace(input.Key)
	if input.Key == "" {
		writeError(w, 400, "BAD_REQUEST", "key is required")
		return
	}
	database := input.Database
	if database == "" {
		database = connection.Database
	}
	quote := func(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
	statement := "DELETE FROM " + quote(database) + "." + quote(input.Key) + ` WHERE "__rowset_key__" IS NOT NULL`
	raw, _ := json.Marshal(input)
	target, ctx, cancel, run := s.nosqlWriteGuard(w, r, connection, database, statement, string(raw))
	if !run {
		return
	}
	defer cancel()
	started := time.Now()
	deleted, err := s.engines.RedisDelete(ctx, target, input)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.recordActivity(r, connection.ID, string(raw), "error", 0, duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", deleted, duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, map[string]any{"deletedCount": deleted, "durationMs": duration})
}
