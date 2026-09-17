package api

import (
	"context"
	"net/http"
	"time"

	"github.com/dbaopsio/rowset-studio/rowset-core/internal/domain"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/engine"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/policy"
	sqlguard "github.com/dbaopsio/rowset-studio/rowset-parser"
)

// nosqlWriteGuard evaluates a synthetic INSERT/UPDATE/DELETE statement
// against the same governance rules real SQL writes go through (read-only
// role/connection, disabled/enabled policy names, custom policies) before a
// MongoDB, Redis/Valkey or Elasticsearch write runs. statementSQL is never
// executed; it only classifies the operation for the policy engine, the way
// the read-side document/key guardrails already do. It writes the HTTP
// response itself and returns ok=false when the write must not proceed.
func (s *Server) nosqlWriteGuard(w http.ResponseWriter, r *http.Request, connection domain.Connection, database, statementSQL, rawBody string) (target engine.Connection, ctx context.Context, cancel context.CancelFunc, ok bool) {
	identity := identityFromContext(r.Context())
	role, err := s.store.UserRole(r.Context(), identity.UserID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "role missing")
		return engine.Connection{}, nil, nil, false
	}
	info, err := sqlguard.ParseDialect(sqlguard.DialectPostgres, statementSQL)
	if err != nil {
		writeError(w, 400, "BAD_REQUEST", err.Error())
		return engine.Connection{}, nil, nil, false
	}
	disabled, enabled, _, timeout, err := s.resolvePolicies(r, identity, connection)
	if err != nil {
		writeError(w, 500, "INTERNAL", "policies unavailable")
		return engine.Connection{}, nil, nil, false
	}
	decision := policy.Evaluate(policy.Input{Statement: info, Role: role.Name, ReadOnly: role.IsReadOnly || connection.ReadOnly, Environment: connection.Environment, Disabled: disabled, Enabled: enabled})
	decision, _, err = s.applyCustomPolicies(r, identity, connection, info, false, decision, 0)
	if err != nil {
		writeError(w, 500, "INTERNAL", "policies unavailable")
		return engine.Connection{}, nil, nil, false
	}
	if decision.Effect != policy.Allow {
		s.recordActivity(r, connection.ID, rawBody, "blocked", 0, 0, "", "", auditMeta{decision: "deny", reason: decision.Reason, policyID: decision.PolicyID})
		writePolicyError(w, 403, "POLICY_DENIED", decision, nil)
		return engine.Connection{}, nil, nil, false
	}
	target, err = s.engineConnection(r, connection, database)
	if err != nil {
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return engine.Connection{}, nil, nil, false
	}
	ctx, cancel = withConnectionTimeout(r, connection, timeout, 10*time.Minute)
	return target, ctx, cancel, true
}
