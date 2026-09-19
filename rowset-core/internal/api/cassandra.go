package api

import (
	"net/http"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/policy"
	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

// cassandraQuery runs one governed CQL statement or one governed CQL batch.
func (s *Server) cassandraQuery(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	identity := identityFromContext(r.Context())
	if connection.Engine != "cassandra" {
		writeError(w, 400, "BAD_REQUEST", "connection is not Cassandra")
		return
	}
	var input struct {
		engine.CassandraQueryInput
		Backup bool `json:"backup"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	info, parseErr := sqlguard.ParseCQL(input.Query)
	if parseErr != nil {
		s.recordActivity(r, connection.ID, input.Query, "parse_error", 0, 0, "", "", auditMeta{decision: "deny", reason: parseErr.Error(), policyID: "parse_error", errorMessage: parseErr.Error()})
		writeError(w, 400, "PARSE_ERROR", parseErr.Error())
		return
	}
	if info.Kind == sqlguard.Session {
		writeError(w, 400, "SESSION_CONTROL_UNSUPPORTED", "session-control CQL is not supported; choose the keyspace in the database selector")
		return
	}
	if input.Limit == 0 {
		input.Limit = 1000
	}
	if input.Limit < 1 || input.Limit > 10000 {
		writeError(w, 400, "BAD_REQUEST", "limit must be between 1 and 10000")
		return
	}
	database := input.Keyspace
	if database == "" {
		database = connection.Database
	}
	input.Keyspace = database
	role, err := s.role(r.Context(), identity.UserID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "role missing")
		return
	}
	disabled, enabled, rowLimit, timeout, err := s.resolvePolicies(r, identity, connection)
	if err != nil {
		writeError(w, 500, "INTERNAL", "policies unavailable")
		return
	}
	if rowLimit > 0 && input.Limit > rowLimit {
		input.Limit = rowLimit
	}
	decision := policy.Evaluate(policy.Input{Statement: info, Role: role.Name, ReadOnly: role.ReadOnly || connection.ReadOnly, Environment: connection.Environment, Disabled: disabled, Enabled: enabled})
	if !s.config.Shared || !identity.IsAdmin() {
		if decision, rowLimit, err = s.applyCustomPolicies(r, identity, connection, info, false, decision, rowLimit); err != nil {
			writeError(w, 500, "INTERNAL", "governance rules unavailable")
			return
		}
		if rowLimit > 0 && input.Limit > rowLimit {
			input.Limit = rowLimit
		}
	}
	if decision.Effect != policy.Allow {
		s.recordActivity(r, connection.ID, input.Query, "blocked", 0, 0, "", sqlguard.ExactHash(input.Query), auditMeta{decision: "deny", reason: decision.Reason, policyID: decision.PolicyID})
		writePolicyError(w, http.StatusForbidden, "POLICY_DENIED", decision, nil)
		return
	}
	target, err := s.engineConnection(r, connection, database)
	if err != nil {
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	ctx, cancel := withConnectionTimeout(r, connection, timeout, 10*time.Minute)
	defer cancel()
	var backupAnnotations Annotations
	if input.Backup {
		backupAnnotations = s.cassandraCaptureBackup(ctx, identity, connection, info, target, database, input.Query)
		if reason, ok := backupAnnotations["backupBlocked"].(string); ok {
			writeError(w, http.StatusConflict, "BACKUP_UNAVAILABLE", reason)
			return
		}
	}
	started := time.Now()
	result, err := s.engines.CassandraQuery(ctx, target, input.CassandraQueryInput)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.discardRowBackup(identity, backupAnnotations)
		s.recordActivity(r, connection.ID, input.Query, "error", 0, duration, "", sqlguard.ExactHash(input.Query), auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	rowCount := int64(len(result.Rows))
	if result.RowsAffected > rowCount {
		rowCount = result.RowsAffected
	}
	if info.Kind == sqlguard.DDL {
		s.engines.InvalidateSchema(connection.ID)
	}
	s.recordActivity(r, connection.ID, input.Query, "success", rowCount, duration, "", sqlguard.ExactHash(input.Query), auditMeta{decision: "allow"})
	response := map[string]any{"columns": result.Columns, "rows": result.Rows, "rowsAffected": result.RowsAffected, "durationMs": result.DurationMS, "truncated": result.Truncated}
	writeJSON(w, 200, backupAnnotations.addTo(response))
}
