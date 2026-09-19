package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/policy"
	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

type explainInput struct {
	SQL      string  `json:"sql"`
	Database string  `json:"database"`
	NodeRole *string `json:"nodeRole"`
	// Analyze runs the statement to measure actual rows and timings.
	Analyze bool `json:"analyze"`
}

// explainQuery returns the execution plan of one statement. Explaining does
// not run the statement; analyzing does, so only SELECT statements may be
// analyzed and policies apply to them as to any query.
func (s *Server) explainQuery(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	var input explainInput
	if !decodeJSON(w, r, &input) {
		return
	}
	sql := strings.TrimSpace(input.SQL)
	if sql == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "sql is required")
		return
	}
	if input.NodeRole != nil && *input.NodeRole != "primary" && *input.NodeRole != "secondary" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "nodeRole must be primary or secondary")
		return
	}
	info, err := sqlguard.ParseDialect(sqlguard.DialectForEngine(connection.Engine), sql)
	if err != nil {
		writeError(w, http.StatusBadRequest, "PARSE_ERROR", err.Error())
		return
	}
	switch info.Kind {
	case sqlguard.Multi:
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Explain one statement at a time")
		return
	case sqlguard.Session, sqlguard.Unknown:
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "This statement has no execution plan")
		return
	}
	if input.Analyze && info.Kind != sqlguard.Select {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Only SELECT statements can be explained with actual rows, because they run")
		return
	}
	identity := identityFromContext(r.Context())
	role, err := s.store.UserRole(r.Context(), identity.UserID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "role missing")
		return
	}
	disabled, enabled, _, policyTimeout, err := s.resolvePolicies(r, identity, connection)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "governance rules unavailable")
		return
	}
	normalized, queryHash := sqlguard.Normalize(info)
	if input.Analyze {
		decision := policy.Evaluate(policy.Input{Statement: info, Role: role.Name, ReadOnly: role.IsReadOnly || connection.ReadOnly, Environment: connection.Environment, Disabled: disabled, Enabled: enabled})
		if !s.config.Shared || !identity.IsAdmin() {
			if decision, _, err = s.applyCustomPolicies(r, identity, connection, info, false, decision, 0); err != nil {
				writeError(w, http.StatusInternalServerError, "INTERNAL", "governance rules unavailable")
				return
			}
		}
		if decision.Effect != policy.Allow {
			s.recordActivity(r, connection.ID, sql, "blocked", 0, 0, normalized, queryHash, auditMeta{decision: "deny", reason: decision.Reason, policyID: decision.PolicyID})
			writePolicyError(w, http.StatusForbidden, "POLICY_DENIED", decision, nil)
			return
		}
	}
	prepared, _, err := s.prepareStatement(r.Context(), StatementRequest{Identity: identity, RoleID: role.ID, Connection: connection, Statement: info})
	if err != nil {
		writeError(w, http.StatusBadRequest, "STATEMENT_REJECTED", err.Error())
		return
	}
	target, _, err := s.routedEngineConnection(r.Context(), identity, connection, strings.TrimSpace(input.Database), input.NodeRole, &prepared)
	if err != nil {
		if errors.Is(err, errSecondaryUnsafe) {
			writeError(w, http.StatusForbidden, "POLICY_DENIED", err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "EXEC_ERROR", err.Error())
		return
	}
	ctx, cancel := withConnectionTimeout(r, connection, policyTimeout, 2*time.Minute)
	defer cancel()
	started := time.Now()
	plan, format, err := s.engines.Explain(ctx, target, prepared.Raw, input.Analyze)
	duration := elapsedMilliseconds(started)
	if errors.Is(err, engine.ErrActualPlanUnsupported) {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	if err != nil {
		s.recordActivity(r, connection.ID, sql, "error", 0, duration, normalized, queryHash, auditMeta{decision: "allow", errorMessage: err.Error()})
		writeStatementError(w, err, prepared.Raw, nil)
		return
	}
	s.recordActivity(r, connection.ID, sql, "explain", 0, duration, normalized, queryHash, auditMeta{decision: "allow"})
	writeJSON(w, http.StatusOK, map[string]any{"engine": connection.Engine, "format": format, "analyzed": input.Analyze, "plan": plan})
}
