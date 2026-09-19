package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/policy"
	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

// Statements from other clients: code that is not the HTTP API (a database
// protocol gateway, for example) runs statements for a signed-in user
// through the same pipeline: access, policies, statement and result hooks,
// row limits, node routing and activity recording.

// Client describes where a statement came from, for the activity log.
type Client struct {
	Type, IP, UserAgent string
}

// Outcome is the result of Run: rows to stream, a buffered result, or a
// refusal (Code and Message, with Decision when a policy refused it).
type Outcome struct {
	Result        engine.Result
	Stream        *ResultStream
	Decision      *policy.Decision
	Code, Message string
}

// Session is one pinned database session, for clients that keep
// transactions open across statements.
type Session struct {
	backend  *engine.Session
	nodeRole string
}

func (s *Session) Begin(options engine.TransactionOptions) error {
	if s.nodeRole == "secondary" {
		return errSecondaryTxn
	}
	return s.backend.Begin(options)
}
func (s *Session) InTransaction() bool { return s != nil && s.backend.InTransaction() }
func (s *Session) Commit() error       { return s.backend.Commit() }
func (s *Session) Rollback() error     { return s.backend.Rollback() }
func (s *Session) Close() error        { return s.backend.Close() }

// ResultStream hands rows to the caller one at a time, with result hook
// transforms applied, and stops at the policy row limit. Close it when the
// caller stops reading early.
type ResultStream struct {
	stream        *engine.RowStream
	columns       []string
	databaseTypes []string
	transforms    ResultTransforms
	annotations   Annotations
	rowLimit      int
	rows          int64
	truncated     bool
	cancel        context.CancelFunc
	finish        func(rows, duration int64, truncated bool, streamErr error)
	once          sync.Once
}

func (s *ResultStream) Columns() []string       { return append([]string(nil), s.columns...) }
func (s *ResultStream) DatabaseTypes() []string { return append([]string(nil), s.databaseTypes...) }

// Transformed reports whether a result hook changes the values of a column.
func (s *ResultStream) Transformed(index int) bool { _, ok := s.transforms[index]; return ok }

// Annotations are what the hooks report about the result, keyed by the
// annotation they were registered with.
func (s *ResultStream) Annotations() Annotations { return s.annotations }
func (s *ResultStream) RowCount() int64          { return s.rows }
func (s *ResultStream) Truncated() bool          { return s.truncated }
func (s *ResultStream) DurationMS() int64        { return s.stream.DurationMS() }

func (s *ResultStream) Next() ([]any, bool, error) {
	row, ok, err := s.stream.Next()
	if err != nil {
		s.finalize(err)
		return nil, false, err
	}
	if !ok {
		s.finalize(nil)
		return nil, false, nil
	}
	if s.rowLimit > 0 && s.rows >= int64(s.rowLimit) {
		s.truncated = true
		s.finalize(nil)
		return nil, false, nil
	}
	s.transforms.apply(row)
	s.rows++
	return row, true, nil
}

func (s *ResultStream) Close() error {
	err := s.stream.Close()
	s.finalize(err)
	return err
}

func (s *ResultStream) finalize(streamErr error) {
	s.once.Do(func() {
		// Cancel before closing, the order Studio uses when it stops a result
		// at its row limit, so the database is told to stop first.
		s.cancel()
		_ = s.stream.Close()
		s.finish(s.rows, s.stream.DurationMS(), s.truncated, streamErr)
	})
}

// CurrentIdentity is a user's identity as it stands now; it fails for a user
// who is no longer active.
func (s *Server) CurrentIdentity(ctx context.Context, userID string) (domain.Identity, error) {
	user, err := s.store.User(ctx, userID)
	if err != nil || user.Status != "active" {
		return domain.Identity{}, errors.New("user is no longer active")
	}
	role, err := s.role(ctx, user.ID)
	if err != nil {
		return domain.Identity{}, errors.New("user has no role")
	}
	return domain.Identity{UserID: user.ID, OrgID: user.OrgID, Email: user.Email, Role: role.Name}, nil
}

// CanUseConnection reports whether the user may use a connection.
func (s *Server) CanUseConnection(ctx context.Context, identity domain.Identity, connection domain.Connection) bool {
	return s.canUseConnection(ctx, identity, connection)
}

// clientRequest carries the caller into the policy helpers, which read it
// from a request.
func clientRequest(ctx context.Context, identity domain.Identity, client Client) *http.Request {
	request := (&http.Request{RemoteAddr: client.IP, Header: make(http.Header)}).WithContext(context.WithValue(ctx, contextKey{}, identity))
	request.Header.Set("User-Agent", client.UserAgent)
	return request
}

// OpenSession opens a pinned session on the node the user's access selects.
func (s *Server) OpenSession(ctx context.Context, identity domain.Identity, connection domain.Connection) (*Session, error) {
	if !s.canUseConnection(ctx, identity, connection) {
		return nil, errors.New("role has no access to this connection")
	}
	desired, err := s.desiredNodeRole(ctx, identity, connection.ID, nil)
	if err != nil {
		return nil, err
	}
	node, err := s.routeNode(ctx, connection, desired)
	if err != nil {
		return nil, err
	}
	target, err := s.engineConnectionAt(ctx, connection, "", node.Host, node.Port)
	if err != nil {
		return nil, err
	}
	openCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	backend, err := s.engines.OpenSession(openCtx, target)
	if err != nil {
		return nil, err
	}
	return &Session{backend: backend, nodeRole: desired}, nil
}

// Describe returns the columns a SELECT would return, without reading rows.
func (s *Server) Describe(ctx context.Context, identity domain.Identity, connection domain.Connection, sql string) ([]string, error) {
	if !s.canUseConnection(ctx, identity, connection) {
		return nil, errors.New("role has no access to this connection")
	}
	info, err := sqlguard.ParseDialect(sqlguard.DialectForEngine(connection.Engine), sql)
	if err != nil || info.Kind != sqlguard.Select {
		return nil, errors.New("statement does not return rows")
	}
	role, err := s.role(ctx, identity.UserID)
	if err != nil {
		return nil, err
	}
	request := clientRequest(ctx, identity, Client{})
	disabled, enabled, _, _, err := s.resolvePolicies(request, identity, connection)
	if err != nil {
		return nil, errors.New("governance rules unavailable")
	}
	decision := policy.Evaluate(policy.Input{Statement: info, Role: role.Name, ReadOnly: role.ReadOnly || connection.ReadOnly, Environment: connection.Environment, Disabled: disabled, Enabled: enabled})
	if decision.Effect != policy.Allow {
		return nil, errors.New(decision.Reason)
	}
	target, _, err := s.routedEngineConnection(ctx, identity, connection, "", nil, &info)
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sql), ";"))
	if connection.Engine == "mssql" {
		query = "SELECT TOP (0) * FROM (" + query + ") AS rowset_describe"
	} else {
		query = "SELECT * FROM (" + query + ") AS rowset_describe LIMIT 0"
	}
	result, err := s.engines.Execute(ctx, target, query, 1)
	if err != nil {
		return nil, err
	}
	return result.Columns, nil
}

// Run runs one statement for a user. With a session it runs there, otherwise
// on a pooled connection to the node the user's access selects.
func (s *Server) Run(ctx context.Context, identity domain.Identity, connection domain.Connection, sql string, client Client, session *Session) Outcome {
	if !s.canUseConnection(ctx, identity, connection) {
		return Outcome{Code: "UNAUTHORIZED", Message: "role has no access to this connection"}
	}
	role, err := s.role(ctx, identity.UserID)
	if err != nil {
		return Outcome{Code: "UNAUTHORIZED", Message: "role missing"}
	}
	info, err := sqlguard.ParseDialect(sqlguard.DialectForEngine(connection.Engine), sql)
	if err != nil {
		return Outcome{Code: "PARSE_ERROR", Message: err.Error()}
	}
	record := func(rows, duration int64, command bool, status string, decision policy.Decision, decisionName, message string) {
		normalized, hash := sqlguard.Normalize(info)
		s.recordClientActivity(ctx, identity, connection, sql, normalized, hash, client.Type, client.IP, client.UserAgent, rows, duration, command, status, decision, decisionName, "", message)
	}
	refuse := func(decision policy.Decision) Outcome {
		record(0, 0, false, "blocked", decision, string(decision.Effect), "")
		return Outcome{Decision: &decision, Code: "POLICY_DENIED", Message: decision.Reason}
	}
	if session != nil && session.nodeRole == "secondary" && !sqlguard.SecondarySafe(info) {
		return refuse(policy.Decision{Effect: policy.Deny, PolicyID: "secondary_read_only", Reason: errSecondaryUnsafe.Error(), Risk: policy.High})
	}
	request := clientRequest(ctx, identity, client)
	disabled, enabled, rowLimit, policyTimeout, err := s.resolvePolicies(request, identity, connection)
	if err != nil {
		return Outcome{Code: "INTERNAL", Message: "governance rules unavailable"}
	}
	decision := policy.Evaluate(policy.Input{Statement: info, Role: role.Name, ReadOnly: role.ReadOnly || connection.ReadOnly, Environment: connection.Environment, Disabled: disabled, Enabled: enabled})
	if !s.config.Shared || !identity.IsAdmin() {
		decision, rowLimit, err = s.applyCustomPolicies(request, identity, connection, info, false, decision, rowLimit)
		if err != nil {
			return Outcome{Code: "INTERNAL", Message: "governance rules unavailable"}
		}
	}
	// A statement held for review cannot wait on a client that expects an
	// answer now, so anything but Allow is refused here.
	if decision.Effect != policy.Allow {
		return refuse(decision)
	}
	prepared, statementNotes, err := s.prepareStatement(ctx, StatementRequest{Identity: identity, RoleID: role.ID, Connection: connection, Statement: info})
	if err != nil {
		return Outcome{Code: "STATEMENT_HOOK_ERROR", Message: err.Error()}
	}
	effective := prepared.Raw
	var target engine.Connection
	if session == nil {
		target, _, err = s.routedEngineConnection(ctx, identity, connection, "", nil, &prepared)
		if err != nil {
			if errors.Is(err, errSecondaryUnsafe) {
				return refuse(policy.Decision{Effect: policy.Deny, PolicyID: "secondary_read_only", Reason: err.Error(), Risk: policy.High})
			}
			return Outcome{Code: "EXEC_ERROR", Message: err.Error()}
		}
	}
	timeout := time.Duration(connection.QueryTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	if policyTimeout > 0 && time.Duration(policyTimeout)*time.Second < timeout {
		timeout = time.Duration(policyTimeout) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	allow := policy.Decision{Effect: policy.Allow, Risk: policy.Low}
	// A row limit is applied while reading: the statement runs as written and
	// reading stops once the limit is reached.
	if engine.ReturnsRows(effective) {
		started := time.Now()
		var stream *engine.RowStream
		if session != nil {
			stream, err = session.backend.Query(runCtx, effective)
		} else {
			stream, err = s.engines.Query(runCtx, target, effective)
		}
		if err != nil {
			cancel()
			record(0, elapsedMilliseconds(started), false, "error", allow, "allow", err.Error())
			return Outcome{Code: "EXEC_ERROR", Message: err.Error()}
		}
		transforms, resultNotes, err := s.prepareResult(ctx, ResultRequest{Identity: identity, Connection: connection, Statement: prepared, Columns: stream.Columns(), Origins: stream.ColumnOrigins()})
		if err != nil {
			_ = stream.Close()
			cancel()
			record(0, stream.DurationMS(), false, "error", policy.Decision{Effect: policy.Deny, Risk: policy.High}, "deny", err.Error())
			return Outcome{Code: "INTERNAL", Message: "failed to apply result rules"}
		}
		result := &ResultStream{stream: stream, columns: stream.Columns(), databaseTypes: stream.DatabaseTypes(), transforms: transforms, annotations: statementNotes.merge(resultNotes), rowLimit: rowLimit, cancel: cancel}
		result.finish = func(rows, duration int64, truncated bool, streamErr error) {
			status, message := "success", ""
			if truncated {
				status = "truncated"
			}
			if streamErr != nil {
				status, message = "error", streamErr.Error()
			}
			record(rows, duration, false, status, decision, "allow", message)
		}
		return Outcome{Stream: result}
	}
	defer cancel()
	started := time.Now()
	var result engine.Result
	if session != nil {
		result, err = session.backend.Execute(runCtx, effective, 0)
	} else {
		result, err = s.engines.Execute(runCtx, target, effective, 0)
	}
	if err != nil {
		duration := result.DurationMS
		if duration == 0 {
			duration = elapsedMilliseconds(started)
		}
		record(0, duration, false, "error", allow, "allow", err.Error())
		return Outcome{Code: "EXEC_ERROR", Message: err.Error()}
	}
	if len(result.Columns) > 0 {
		transforms, _, err := s.prepareResult(ctx, ResultRequest{Identity: identity, Connection: connection, Statement: prepared, Columns: result.Columns})
		if err != nil {
			record(0, result.DurationMS, false, "error", policy.Decision{Effect: policy.Deny, Risk: policy.High}, "deny", err.Error())
			return Outcome{Code: "INTERNAL", Message: "failed to apply result rules"}
		}
		for _, row := range result.Rows {
			transforms.apply(row)
		}
	}
	if rowLimit > 0 && len(result.Rows) > rowLimit {
		result.Rows = result.Rows[:rowLimit]
		result.Truncated = true
	}
	rows := int64(len(result.Rows))
	command := len(result.Columns) == 0
	if command {
		rows = result.RowsAffected
	}
	status := "success"
	if result.Truncated {
		status = "truncated"
	}
	record(rows, result.DurationMS, command, status, decision, "allow", "")
	return Outcome{Result: result}
}
