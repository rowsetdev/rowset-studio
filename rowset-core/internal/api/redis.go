package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/policy"
	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

func (s *Server) redisScan(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	identity := identityFromContext(r.Context())
	if connection.Engine != "redis" && connection.Engine != "valkey" {
		writeError(w, 400, "UNSUPPORTED", "this connection is not a Redis or Valkey connection")
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
	info := nosqlStatement(sqlguard.Select, database, pattern, pattern != "*")
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
	if connection.Engine != "redis" && connection.Engine != "valkey" {
		writeError(w, 400, "UNSUPPORTED", "this connection is not a Redis or Valkey connection")
		return
	}
	var input struct {
		engine.RedisWriteInput
		Backup bool `json:"backup"`
	}
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
	// SET/HSET always targets exactly one named key, so HasWhere is always
	// true here: there is no equivalent of an unscoped mass UPDATE.
	info := nosqlStatement(sqlguard.Update, database, input.Key, true)
	raw, _ := json.Marshal(input)
	target, ctx, cancel, run := s.nosqlWriteGuard(w, r, connection, database, info, string(raw))
	if !run {
		return
	}
	defer cancel()
	var backupAnnotations Annotations
	if input.Backup {
		backupAnnotations = s.redisCaptureBackup(ctx, identity, connection, target, database, input.Key, "update", string(raw))
	}
	started := time.Now()
	err := s.engines.RedisWrite(ctx, target, input.RedisWriteInput)
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

// redisBulkWrite is redisWrite for several keys in one pipelined write (up
// to 10,000). No row backup: capturing a DUMP per key here could mean
// thousands of snapshots for one bulk write, which the SQL/Mongo bulk paths
// don't do either.
func (s *Server) redisBulkWrite(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	if connection.Engine != "redis" && connection.Engine != "valkey" {
		writeError(w, 400, "UNSUPPORTED", "this connection is not a Redis or Valkey connection")
		return
	}
	var input engine.RedisBulkWriteInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.Writes) == 0 {
		writeError(w, 400, "BAD_REQUEST", "at least one write is required")
		return
	}
	if len(input.Writes) > 10000 {
		writeError(w, 400, "BAD_REQUEST", "at most 10000 writes can run at once")
		return
	}
	database := input.Database
	if database == "" {
		database = connection.Database
	}
	// Classified the same way one write is: a named key, always scoped.
	info := nosqlStatement(sqlguard.Update, database, fmt.Sprintf("%d keys", len(input.Writes)), true)
	raw, _ := json.Marshal(input)
	target, ctx, cancel, run := s.nosqlWriteGuard(w, r, connection, database, info, string(raw))
	if !run {
		return
	}
	defer cancel()
	started := time.Now()
	written, err := s.engines.RedisBulkWrite(ctx, target, input)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.recordActivity(r, connection.ID, string(raw), "error", int64(written), duration, "", "", auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, string(raw), "success", int64(len(input.Writes)), duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, 200, map[string]any{"written": len(input.Writes), "durationMs": duration})
}

func (s *Server) redisDelete(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	identity := identityFromContext(r.Context())
	if connection.Engine != "redis" && connection.Engine != "valkey" {
		writeError(w, 400, "UNSUPPORTED", "this connection is not a Redis or Valkey connection")
		return
	}
	var input struct {
		engine.RedisDeleteInput
		Backup bool `json:"backup"`
	}
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
	// DEL always targets exactly one named key.
	info := nosqlStatement(sqlguard.Delete, database, input.Key, true)
	raw, _ := json.Marshal(input)
	target, ctx, cancel, run := s.nosqlWriteGuard(w, r, connection, database, info, string(raw))
	if !run {
		return
	}
	defer cancel()
	var backupAnnotations Annotations
	if input.Backup {
		backupAnnotations = s.redisCaptureBackup(ctx, identity, connection, target, database, input.Key, "delete", string(raw))
	}
	started := time.Now()
	deleted, err := s.engines.RedisDelete(ctx, target, input.RedisDeleteInput)
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
