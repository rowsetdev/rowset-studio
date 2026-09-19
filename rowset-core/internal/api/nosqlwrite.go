package api

import (
	"context"
	"net/http"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/policy"
	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

// nosqlStatement builds the sqlguard.Info a MongoDB/Redis/Elasticsearch
// operation is classified by, directly as Go values rather than assembling
// SQL text and reparsing it. That round trip (build a fake "UPDATE ... WHERE
// ..." string, then parse it back) is exactly what let a hardcoded WHERE
// placeholder silently defeat the without-WHERE guardrail: a struct literal
// can't "forget" to interpolate a clause the way string concatenation can.
// object is the collection/key/index name, addressed as one table so
// per-table custom policies (deny_table, deny_schema) still match it.
func nosqlStatement(kind sqlguard.Kind, database, object string, hasWhere bool) sqlguard.Info {
	return sqlguard.Info{Kind: kind, Tables: []sqlguard.TableRef{{Schema: database, Name: object}}, HasWhere: hasWhere}
}

// nosqlPolicyAllowed evaluates a synthetic INSERT/UPDATE/DELETE
// classification against the same governance rules real SQL writes go
// through (read-only role/connection, disabled/enabled policy names, custom
// policies). info is never turned back into SQL or executed; it only
// classifies the operation for the policy engine, the way the read-side
// document/key guardrails already do. It writes the HTTP response itself
// and returns ok=false when the write must not proceed; on true it also
// returns the resolved policy timeout, for callers that still need to open
// a connection themselves.
func (s *Server) nosqlPolicyAllowed(w http.ResponseWriter, r *http.Request, connection domain.Connection, info sqlguard.Info, rawBody string) (timeout int, ok bool) {
	identity := identityFromContext(r.Context())
	role, err := s.store.UserRole(r.Context(), identity.UserID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "role missing")
		return 0, false
	}
	disabled, enabled, _, timeout, err := s.resolvePolicies(r, identity, connection)
	if err != nil {
		writeError(w, 500, "INTERNAL", "policies unavailable")
		return 0, false
	}
	decision := policy.Evaluate(policy.Input{Statement: info, Role: role.Name, ReadOnly: role.IsReadOnly || connection.ReadOnly, Environment: connection.Environment, Disabled: disabled, Enabled: enabled})
	decision, _, err = s.applyCustomPolicies(r, identity, connection, info, false, decision, 0)
	if err != nil {
		writeError(w, 500, "INTERNAL", "policies unavailable")
		return 0, false
	}
	if decision.Effect != policy.Allow {
		s.recordActivity(r, connection.ID, rawBody, "blocked", 0, 0, "", "", auditMeta{decision: "deny", reason: decision.Reason, policyID: decision.PolicyID})
		writePolicyError(w, 403, "POLICY_DENIED", decision, nil)
		return 0, false
	}
	return timeout, true
}

// nosqlWriteGuard is nosqlPolicyAllowed plus opening the connection, for a
// standalone (non-transaction) write.
func (s *Server) nosqlWriteGuard(w http.ResponseWriter, r *http.Request, connection domain.Connection, database string, info sqlguard.Info, rawBody string) (target engine.Connection, ctx context.Context, cancel context.CancelFunc, ok bool) {
	timeout, allowed := s.nosqlPolicyAllowed(w, r, connection, info, rawBody)
	if !allowed {
		return engine.Connection{}, nil, nil, false
	}
	target, err := s.engineConnection(r, connection, database)
	if err != nil {
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return engine.Connection{}, nil, nil, false
	}
	ctx, cancel = withConnectionTimeout(r, connection, timeout, 10*time.Minute)
	return target, ctx, cancel, true
}
