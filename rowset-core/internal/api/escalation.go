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

// Escalation handles statements a policy holds for review. Without one, such
// statements are refused like any other policy denial.
type Escalation interface {
	// Cleared reports whether this exact statement was already cleared for
	// the caller on the connection, and its reference.
	Cleared(ctx context.Context, identity domain.Identity, connectionID, exactHash string) (reference string, ok bool)
	// Request records that the statement awaits review and returns the body
	// of the accepted response.
	Request(ctx context.Context, identity domain.Identity, connection domain.Connection, sql, queryHash, exactHash string, decision policy.Decision) (reference string, response map[string]any, err error)
	// Consume marks a cleared reference as used.
	Consume(ctx context.Context, reference string)
	// DenialDetails returns fields added to a policy denial of the statement.
	DenialDetails(identity domain.Identity, statement sqlguard.Info) map[string]any
}

// SetEscalation installs the escalation handler; call before Handler.
func (s *Server) SetEscalation(escalation Escalation) { s.escalation = escalation }

// DeferredStatement is a stored statement executed later on behalf of its
// author. Source names the client type recorded in activity.
type DeferredStatement struct {
	Reference, UserID, ConnectionID, SQL, Source string
}

type DeferredResult struct {
	Status     string
	Rows       int64
	DurationMS int64
	Error      string
}

func deferredError(message string) DeferredResult {
	return DeferredResult{Status: "error", Error: message}
}

// ExecuteDeferred runs a cleared statement for its author through the same
// pipeline as a live query: access, policies, statement hooks, row limits,
// routing to the primary and activity recording.
func (s *Server) ExecuteDeferred(r *http.Request, statement DeferredStatement) DeferredResult {
	info, err := sqlguard.Parse(statement.SQL)
	if err != nil {
		return deferredError(err.Error())
	}
	user, err := s.store.User(r.Context(), statement.UserID)
	if err != nil {
		return deferredError("requesting user no longer exists")
	}
	role, err := s.store.UserRole(r.Context(), user.ID)
	if err != nil {
		return deferredError("requesting user has no role")
	}
	connection, err := s.store.Connection(r.Context(), statement.ConnectionID)
	if err != nil {
		return deferredError("connection no longer exists")
	}
	access := role.Name == "admin"
	if !access {
		items, _ := s.store.ListRoleConnectionAccess(r.Context(), role.ID)
		for _, item := range items {
			access = access || item.ConnectionID == connection.ID
		}
	}
	if !access {
		return deferredError("requesting user no longer has connection access")
	}
	caller := domain.Identity{UserID: user.ID, OrgID: user.OrgID, Email: user.Email, Role: role.Name}
	disabled, enabled, rowLimit, policyTimeout, err := s.resolvePolicies(r, caller, connection)
	if err != nil {
		return deferredError("governance rules unavailable")
	}
	decision := policy.Evaluate(policy.Input{Statement: info, Role: role.Name, ReadOnly: role.IsReadOnly || connection.ReadOnly, Environment: connection.Environment, Cleared: true, Disabled: disabled, Enabled: enabled})
	if !caller.IsAdmin() {
		decision, rowLimit, err = s.applyCustomPolicies(r, caller, connection, info, true, decision, rowLimit)
		if err != nil {
			return deferredError("governance rules unavailable")
		}
	}
	if decision.Effect != policy.Allow {
		return deferredError(decision.Reason)
	}
	prepared, _, err := s.prepareStatement(r.Context(), StatementRequest{Identity: caller, RoleID: role.ID, Connection: connection, Statement: info})
	if err != nil {
		return deferredError(err.Error())
	}
	// The statement runs as written; a row cap is applied while reading.
	sql := prepared.Raw
	primary := "primary"
	target, _, err := s.routedEngineConnection(r.Context(), caller, connection, "", &primary, &info)
	if err != nil {
		return deferredError(err.Error())
	}
	ctx, cancel := withConnectionTimeout(r, connection, policyTimeout, 10*time.Minute)
	defer cancel()
	normalized, hash := sqlguard.Normalize(info)
	record := func(rows, duration int64, command bool, status, message string) {
		s.recordClientActivity(r.Context(), caller, connection, statement.SQL, normalized, hash, statement.Source, r.RemoteAddr, r.UserAgent(), rows, duration, command, status, decision, "allow", statement.Reference, message)
	}
	if engine.ReturnsRows(sql) {
		stream, err := s.engines.Query(ctx, target, sql)
		if err != nil {
			record(0, 0, false, "error", err.Error())
			return deferredError(err.Error())
		}
		defer stream.Close()
		var rows int64
		for rowLimit <= 0 || rows < int64(rowLimit) {
			_, ok, err := stream.Next()
			if err != nil {
				record(rows, stream.DurationMS(), false, "error", err.Error())
				return DeferredResult{Status: "error", Rows: rows, DurationMS: stream.DurationMS(), Error: err.Error()}
			}
			if !ok {
				break
			}
			rows++
		}
		record(rows, stream.DurationMS(), false, "success", "")
		return DeferredResult{Status: "ok", Rows: rows, DurationMS: stream.DurationMS()}
	}
	result, err := s.engines.Execute(ctx, target, sql, 0)
	if err != nil {
		record(0, result.DurationMS, false, "error", err.Error())
		return DeferredResult{Status: "error", DurationMS: result.DurationMS, Error: err.Error()}
	}
	rows := result.RowsAffected
	if len(result.Columns) > 0 {
		rows = int64(len(result.Rows))
	}
	record(rows, result.DurationMS, len(result.Columns) == 0, "success", "")
	return DeferredResult{Status: "ok", Rows: rows, DurationMS: result.DurationMS}
}

// AuthorizedConnection resolves the {id} path value to a connection the
// caller may use; on failure the error response has been written.
func (k Kit) AuthorizedConnection(w http.ResponseWriter, r *http.Request) (domain.Connection, bool) {
	return k.server.authorizedConnection(w, r)
}

func (k Kit) ExecuteDeferred(r *http.Request, statement DeferredStatement) DeferredResult {
	return k.server.ExecuteDeferred(r, statement)
}
