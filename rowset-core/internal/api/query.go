package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/id"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/policy"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

type queryInput struct {
	MaxRows  int     `json:"maxRows"`
	SQL      string  `json:"sql"`
	Database string  `json:"database"`
	NodeRole *string `json:"nodeRole"`
	// Backup asks for the rows an UPDATE or DELETE changes to be saved first.
	Backup bool `json:"backup"`
}

func (s *Server) runQuery(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	if connection.Engine == "mongodb" {
		writeError(w, 400, "UNSUPPORTED", "use a MongoDB find query in the Query editor")
		return
	}
	var input queryInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.SQL, input.Database = strings.TrimSpace(input.SQL), strings.TrimSpace(input.Database)
	if input.SQL == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "sql is required")
		return
	}
	if input.NodeRole != nil && *input.NodeRole != "primary" && *input.NodeRole != "secondary" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "nodeRole must be primary or secondary")
		return
	}
	s.executeQuery(w, r, connection, input, nil)
}

func (s *Server) executeQuery(w http.ResponseWriter, r *http.Request, connection domain.Connection, input queryInput, transaction *engine.Transaction) {
	if input.MaxRows < 0 || input.MaxRows > 10000 {
		writeError(w, 400, "BAD_REQUEST", "maxRows must be between 0 and 10000")
		return
	}
	identity := identityFromContext(r.Context())
	role, err := s.role(r.Context(), identity.UserID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "role missing")
		return
	}
	info, parseErr := sqlguard.ParseDialect(sqlguard.DialectForEngine(connection.Engine), input.SQL)
	if parseErr != nil {
		s.recordActivity(r, connection.ID, input.SQL, "parse_error", 0, 0, "", "", auditMeta{decision: "deny", reason: parseErr.Error(), policyID: "parse_error", errorMessage: parseErr.Error()})
		writeError(w, http.StatusBadRequest, "PARSE_ERROR", parseErr.Error())
		return
	}
	// HTTP requests do not own a persistent database session. Raw COMMIT/BEGIN
	// could desynchronize a managed transaction or leak session state to the pool.
	if info.Kind == sqlguard.Session {
		message := "Session-control SQL is not supported in the web editor. Use Begin/Commit/Rollback and the database selector."
		s.recordActivity(r, connection.ID, input.SQL, "blocked", 0, 0, "", "", auditMeta{decision: "deny", reason: message, policyID: "web_session_control"})
		writeError(w, http.StatusBadRequest, "SESSION_CONTROL_UNSUPPORTED", message)
		return
	}
	normalized, queryHash := sqlguard.Normalize(info)
	exactHash := sqlguard.ExactHash(input.SQL)
	cleared, reference := false, ""
	if s.escalation != nil {
		reference, cleared = s.escalation.Cleared(r.Context(), identity, connection.ID, exactHash)
	}

	disabled, enabled, rowLimit, policyTimeout, err := s.resolvePolicies(r, identity, connection)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "governance rules unavailable")
		return
	}
	decision := policy.Evaluate(policy.Input{Statement: info, Role: role.Name, ReadOnly: role.ReadOnly || connection.ReadOnly, Environment: connection.Environment, Cleared: cleared, Disabled: disabled, Enabled: enabled})
	if !s.config.Shared || !identity.IsAdmin() {
		decision, rowLimit, err = s.applyCustomPolicies(r, identity, connection, info, cleared, decision, rowLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL", "governance rules unavailable")
			return
		}
	}
	if decision.Effect == policy.Deny || decision.Effect != policy.Allow && s.escalation == nil {
		var details map[string]any
		if s.escalation != nil {
			details = s.escalation.DenialDetails(identity, info)
		}
		s.recordActivity(r, connection.ID, input.SQL, "blocked", 0, 0, normalized, queryHash, auditMeta{decision: "deny", reason: decision.Reason, policyID: decision.PolicyID})
		writePolicyError(w, http.StatusForbidden, "POLICY_DENIED", decision, details)
		return
	}
	if decision.Effect != policy.Allow {
		var response map[string]any
		reference, response, err = s.escalation.Request(r.Context(), identity, connection, input.SQL, queryHash, exactHash, decision)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL", "the statement could not be held for review")
			return
		}
		s.recordActivity(r, connection.ID, input.SQL, "blocked", 0, 0, normalized, queryHash, auditMeta{decision: string(decision.Effect), reason: decision.Reason, policyID: decision.PolicyID, reference: reference})
		writeJSON(w, http.StatusAccepted, response)
		return
	}
	if cleared {
		s.escalation.Consume(r.Context(), reference)
	}

	prepared, annotations, err := s.prepareStatement(r.Context(), StatementRequest{Identity: identity, RoleID: role.ID, Connection: connection, Statement: info})
	if err != nil {
		s.recordActivity(r, connection.ID, input.SQL, "rewrite_error", 0, 0, normalized, queryHash, auditMeta{decision: "deny", reason: err.Error(), policyID: "statement_hook", errorMessage: err.Error()})
		writeError(w, http.StatusBadRequest, "STATEMENT_REJECTED", err.Error())
		return
	}
	info = prepared
	// The statement runs exactly as written. A row cap stops the reading
	// after that many rows instead of rewriting the SQL, so no statement
	// becomes invalid and nothing is executed that the user did not write.
	effectiveSQL := info.Raw
	// Only a policy caps a result on its own; maxRows is what a client asks
	// for, and it can only make the cap smaller.
	if input.MaxRows > 0 && (rowLimit == 0 || input.MaxRows < rowLimit) {
		rowLimit = input.MaxRows
		annotations["limitedBy"] = "request"
	}
	policyCap := rowLimit > 0

	defer s.holdAwake()()
	ctx, cancel := withConnectionTimeout(r, connection, policyTimeout, s.defaultStatementTimeout(10*time.Minute))
	defer cancel()
	var target engine.Connection
	if transaction == nil {
		var targetErr error
		var resolvedRole string
		target, resolvedRole, targetErr = s.routedEngineConnection(r.Context(), identity, connection, input.Database, input.NodeRole, &info)
		if targetErr != nil {
			if errors.Is(targetErr, errSecondaryUnsafe) {
				s.recordActivity(r, connection.ID, input.SQL, "blocked", 0, 0, normalized, queryHash, auditMeta{decision: "deny", reason: targetErr.Error(), policyID: "secondary_read_only"})
				writeError(w, http.StatusForbidden, "POLICY_DENIED", targetErr.Error())
				return
			}
			writeError(w, http.StatusBadGateway, "EXEC_ERROR", targetErr.Error())
			return
		}
		// Which physical node actually ran this - only meaningful once a
		// connection has more than one, but cheap and harmless to always
		// include.
		if target.Host != "" {
			annotations["node"] = map[string]string{"host": target.Host, "role": resolvedRole}
		}
	}
	// The rows a simple UPDATE or DELETE changes can be backed up first. On a
	// shared server only administrators read a backup's script, since it
	// holds values as stored, before result hooks.
	if input.Backup && sqlguard.IsWrite(info.Command) {
		notes := s.captureRowBackup(ctx, identity, connection, info, target, transaction, input.Database)
		if reason, ok := notes["backupBlocked"].(string); ok {
			writeError(w, http.StatusConflict, "BACKUP_UNAVAILABLE", reason)
			return
		}
		annotations.merge(notes)
	}
	if engine.ReturnsRows(effectiveSQL) {
		streamTimeout := time.Duration(s.config.QueryStreamTimeoutSecs) * time.Second
		if streamTimeout <= 0 {
			streamTimeout = 8 * time.Minute
		}
		if !s.config.Shared {
			streamTimeout = s.defaultStatementTimeout(streamTimeout)
		}
		streamCtx, stopStream := context.WithTimeout(ctx, streamTimeout)
		defer stopStream()
		var stream *engine.RowStream
		executionStarted := time.Now()
		if transaction != nil {
			stream, err = transaction.Query(streamCtx, effectiveSQL)
		} else {
			stream, err = s.engines.Query(streamCtx, target, effectiveSQL)
		}
		if err != nil {
			s.recordActivity(r, connection.ID, input.SQL, "error", 0, elapsedMilliseconds(executionStarted), normalized, queryHash, auditMeta{decision: "allow", reference: reference, errorMessage: err.Error()})
			writeStatementError(w, err, effectiveSQL, transaction)
			return
		}
		defer stream.Close()
		transforms, resultNotes, resultErr := s.prepareResult(r.Context(), ResultRequest{Identity: identity, Connection: connection, Statement: info, Columns: stream.Columns(), Origins: stream.ColumnOrigins()})
		if resultErr != nil {
			s.recordActivity(r, connection.ID, input.SQL, "error", 0, stream.DurationMS(), normalized, queryHash, auditMeta{decision: "deny", reference: reference, errorMessage: resultErr.Error()})
			writeError(w, http.StatusInternalServerError, "INTERNAL", "failed to prepare the result")
			return
		}
		// Forgotten before the response as well as after it: a client reloads
		// the schema the moment the last row arrives, which can be before
		// this handler gets past the response.
		s.forgetSchemaAfter(info.Kind, connection.ID)
		s.streamQueryResponse(w, r, connection, input.SQL, normalized, queryHash, reference, stream, transforms, annotations.merge(resultNotes), policyCap, rowLimit)
		s.forgetSchemaAfter(info.Kind, connection.ID)
		// Ending a capped read leaves the database still producing rows;
		// cancelling first lets the driver abort instead of draining them.
		stopStream()
		return
	}
	var result engine.Result
	executionStarted := time.Now()
	if transaction != nil {
		result, err = transaction.Execute(ctx, effectiveSQL, 0)
	} else {
		result, err = s.engines.Execute(ctx, target, effectiveSQL, 0)
	}
	if err != nil {
		duration := result.DurationMS
		if duration == 0 {
			duration = elapsedMilliseconds(executionStarted)
		}
		s.discardRowBackup(identity, annotations)
		s.recordActivity(r, connection.ID, input.SQL, "error", 0, duration, normalized, queryHash, auditMeta{decision: "allow", reference: reference, errorMessage: err.Error()})
		go s.notifyLongStatement(identity.UserID, connection.Name, input.SQL, 0, time.Duration(duration)*time.Millisecond, err.Error())
		writeStatementError(w, err, effectiveSQL, transaction)
		return
	}
	s.forgetSchemaAfter(info.Kind, connection.ID)
	rowCount := result.RowsAffected
	s.recordActivity(r, connection.ID, input.SQL, "success", rowCount, result.DurationMS, normalized, queryHash, auditMeta{decision: "allow", reference: reference, command: true})
	go s.notifyLongStatement(identity.UserID, connection.Name, input.SQL, rowCount, time.Duration(result.DurationMS)*time.Millisecond, "")
	response := map[string]any{"columns": []string{}, "rows": [][]any{}, "rowCount": rowCount, "rowsAffected": result.RowsAffected, "durationMs": result.DurationMS, "truncated": false}
	writeJSON(w, http.StatusOK, annotations.addTo(response))
}

// forgetSchemaAfter drops the cached schema after a statement that can
// create, alter or drop objects — DDL, a script, a procedure call — so the
// explorer and autocomplete show the change straight away.
func (s *Server) forgetSchemaAfter(kind sqlguard.Kind, connectionID string) {
	switch kind {
	case sqlguard.Select, sqlguard.Insert, sqlguard.Update, sqlguard.Delete, sqlguard.Session:
		return
	}
	s.engines.InvalidateSchema(connectionID)
	_ = s.store.DeleteSchemaSnapshots(context.Background(), connectionID)
}

func elapsedMilliseconds(started time.Time) int64 {
	elapsed := time.Since(started).Milliseconds()
	if elapsed == 0 {
		return 1
	}
	return elapsed
}

type queryRowStream interface {
	Columns() []string
	DurationMS() int64
	Next() ([]any, bool, error)
}

func (s *Server) streamQueryResponse(w http.ResponseWriter, r *http.Request, connection domain.Connection, sql, normalized, hash, reference string, stream queryRowStream, transforms ResultTransforms, annotations Annotations, policyCap bool, rowLimit int) {
	if strings.Contains(r.Header.Get("Accept"), "application/x-ndjson") {
		s.streamNDJSON(w, r, connection, sql, normalized, hash, reference, stream, transforms, annotations, policyCap, rowLimit)
		return
	}
	columnsJSON, _ := json.Marshal(stream.Columns())
	typesJSON, _ := json.Marshal(streamColumnTypes(stream))
	originsJSON, _ := json.Marshal(streamColumnOrigins(stream))
	w.Header().Set("Content-Type", "application/json")
	_, writeErr := fmt.Fprintf(w, `{"columns":%s,"columnTypes":%s,"columnOrigins":%s,%s"rows":[`, columnsJSON, typesJSON, originsJSON, annotations.fields())
	rowCount, first, truncated := int64(0), true, false
	var streamErr error
	for writeErr == nil {
		row, ok, err := stream.Next()
		if err != nil {
			streamErr = err
			break
		}
		if !ok {
			break
		}
		if policyCap && rowLimit > 0 && rowCount >= int64(rowLimit) {
			truncated = true
			break
		}
		transforms.apply(row)
		encoded, err := json.Marshal(browserRow(row))
		if err != nil {
			streamErr = err
			break
		}
		prefix := ""
		if !first {
			prefix = ","
		}
		_, writeErr = fmt.Fprintf(w, "%s%s", prefix, encoded)
		first = false
		rowCount++
	}
	if writeErr == nil {
		tail := fmt.Sprintf(`],"rowCount":%d,"truncated":%t,"durationMs":%d`, rowCount, truncated, stream.DurationMS())
		if truncated {
			notice, _ := json.Marshal(limitNotice(annotations, rowLimit))
			tail += fmt.Sprintf(`,"policyNotice":%s`, notice)
		}
		if streamErr != nil {
			message, _ := json.Marshal(streamErr.Error())
			tail += fmt.Sprintf(`,"error":%s`, message)
		}
		_, writeErr = fmt.Fprintln(w, tail+"}")
	}
	status, errorMessage := "success", ""
	if truncated {
		status = "truncated"
	}
	if streamErr != nil || writeErr != nil {
		status = "error"
		if streamErr != nil {
			errorMessage = streamErr.Error()
		} else {
			errorMessage = writeErr.Error()
		}
	}
	s.recordActivity(r, connection.ID, sql, status, rowCount, stream.DurationMS(), normalized, hash, auditMeta{decision: "allow", reference: reference, errorMessage: errorMessage})
	go s.notifyLongStatement(identityFromContext(r.Context()).UserID, connection.Name, sql, rowCount, time.Duration(stream.DurationMS())*time.Millisecond, errorMessage)
}

func (s *Server) resolvePolicies(r *http.Request, identity domain.Identity, connection domain.Connection) (map[string]bool, map[string]bool, int, int, error) {
	disabled, enabled := map[string]bool{}, map[string]bool{}
	items, err := s.store.ListPolicyOverrides(r.Context(), identity.OrgID)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	type override struct {
		key     string
		enabled bool
		config  *string
	}
	var connectionItems, roleItems []override
	apply := func(item override) (int, int) {
		if item.enabled {
			delete(disabled, item.key)
			enabled[item.key] = true
		} else {
			disabled[item.key] = true
			delete(enabled, item.key)
		}
		if item.key == "limit_rows" && !item.enabled {
			return 0, -1
		}
		if item.key == "limit_rows" && item.config != nil {
			value, _ := strconv.Atoi(strings.TrimSpace(*item.config))
			return value, -1
		}
		if item.key == "query_timeout_seconds" && !item.enabled {
			return -1, 0
		}
		if item.key == "query_timeout_seconds" && item.config != nil {
			value, _ := strconv.Atoi(strings.TrimSpace(*item.config))
			return -1, value
		}
		return -1, -1
	}
	rowLimit, queryTimeout := 0, 0
	for _, item := range items {
		key, target, scoped := strings.Cut(item.Key, "@")
		if !scoped {
			if rows, timeout := apply(override{key, item.Enabled, item.Config}); rows >= 0 {
				rowLimit = rows
			} else if timeout >= 0 {
				queryTimeout = timeout
			}
			continue
		}
		if target == connection.ID {
			connectionItems = append(connectionItems, override{key, item.Enabled, item.Config})
		}
		if strings.HasPrefix(target, "role:") && strings.EqualFold(strings.TrimPrefix(target, "role:"), identity.Role) {
			roleItems = append(roleItems, override{key, item.Enabled, item.Config})
		}
	}
	for _, item := range append(connectionItems, roleItems...) {
		if rows, timeout := apply(item); rows >= 0 {
			rowLimit = rows
		} else if timeout >= 0 {
			queryTimeout = timeout
		}
	}
	return disabled, enabled, rowLimit, queryTimeout, nil
}

func (s *Server) applyCustomPolicies(r *http.Request, identity domain.Identity, connection domain.Connection, info sqlguard.Info, cleared bool, decision policy.Decision, limit int) (policy.Decision, int, error) {
	items, err := s.store.ListCustomPolicies(r.Context(), identity.OrgID)
	if err != nil {
		return decision, limit, err
	}
	for _, item := range items {
		if !item.Enabled || item.ConnectionID != nil && *item.ConnectionID != connection.ID || item.RoleName != nil && !strings.EqualFold(*item.RoleName, identity.Role) {
			continue
		}
		matchTable, matchSchema := false, false
		for _, table := range info.Tables {
			matchTable = matchTable || strings.EqualFold(table.Name, strings.TrimSpace(item.Config))
			matchSchema = matchSchema || strings.EqualFold(table.Schema, strings.TrimSpace(item.Config))
		}
		blocks := func(reason string) policy.Decision {
			return policy.Decision{Effect: policy.Deny, PolicyID: "custom:" + item.ID, Reason: reason, Risk: policy.High}
		}
		switch item.Kind {
		case "deny_table":
			if !cleared && matchTable {
				return blocks("policy '" + item.Name + "' blocks table " + item.Config), limit, nil
			}
		case "deny_schema":
			if !cleared && matchSchema {
				return blocks("policy '" + item.Name + "' blocks schema " + item.Config), limit, nil
			}
		case "deny_statement":
			if !cleared && strings.EqualFold(string(info.Kind), strings.TrimSpace(item.Config)) {
				return blocks("policy '" + item.Name + "' blocks " + item.Config + " statements"), limit, nil
			}
		case "limit_rows":
			if info.Kind == sqlguard.Select {
				if value, err := strconv.Atoi(strings.TrimSpace(item.Config)); err == nil && value > 0 && (limit == 0 || value < limit) {
					limit = value
				}
			}
		case "deny_write_outside_hours":
			if !cleared && sqlguard.IsWrite(info.Kind) {
				parts := strings.Split(item.Config, "-")
				if len(parts) == 2 {
					start, firstErr := time.Parse("15:04", strings.TrimSpace(parts[0]))
					end, secondErr := time.Parse("15:04", strings.TrimSpace(parts[1]))
					if firstErr == nil && secondErr == nil {
						now := time.Now()
						minute, startMinute, endMinute := now.Hour()*60+now.Minute(), start.Hour()*60+start.Minute(), end.Hour()*60+end.Minute()
						inside := minute >= startMinute && minute <= endMinute
						if startMinute > endMinute {
							inside = minute >= startMinute || minute <= endMinute
						}
						if !inside {
							return blocks("policy '" + item.Name + "' blocks writes outside " + item.Config), limit, nil
						}
					}
				}
			}
		default:
			if rule, ok := customRules[item.Kind]; ok && !cleared {
				if next, applies := rule.evaluate(item, info, matchTable, decision); applies {
					decision = next
				}
			}
		}
	}
	return decision, limit, nil
}

type auditMeta struct {
	decision, reason, policyID, reference, errorMessage string
	command                                             bool
}

func (s *Server) recordActivity(r *http.Request, connectionID, sql, status string, rows, duration int64, normalized, hash string, meta auditMeta) {
	identity := identityFromContext(r.Context())
	_ = s.activity.CreateQueryHistory(r.Context(), domain.QueryHistory{ID: id.New(), UserID: identity.UserID, ConnectionID: connectionID, SQL: sql, NormalizedSQL: normalized, QueryHash: hash, Status: status, RowsReturned: rows, DurationMS: duration, CreatedAt: store.NowString()})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	entry := domain.AuditLog{ID: id.New(), UserID: identity.UserID, OrgID: identity.OrgID, Role: identity.Role, ConnectionID: connectionID, SQL: sql, NormalizedSQL: normalized, QueryHash: hash, ClientType: "web", ClientIP: r.RemoteAddr, UserAgent: r.UserAgent(), StartedAt: now, DurationMS: duration, PolicyDecision: meta.decision, PolicyReason: meta.reason, PolicyID: meta.policyID, Reference: meta.reference, ErrorMessage: meta.errorMessage, CreatedAt: now}
	if meta.command {
		entry.RowsAffected = rows
		entry.RowsAffectedSet = true
	} else {
		entry.RowsReturned = rows
		entry.RowsReturnedSet = true
	}
	_ = s.activity.WriteAudit(r.Context(), entry)
}

func (s *Server) queryHistory(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	identity := identityFromContext(r.Context())
	from, to := historyRange(r)
	items, err := s.activity.ListQueryHistory(r.Context(), identity.UserID, connection.ID, from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "failed to list history")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": items})
}

// myHistory lists the caller's own statements across every connection. It
// never includes other users' activity.
func (s *Server) myHistory(w http.ResponseWriter, r *http.Request) {
	identity := identityFromContext(r.Context())
	from, to := historyRange(r)
	items, err := s.activity.ListQueryHistory(r.Context(), identity.UserID, "", from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "failed to list history")
		return
	}
	type entry struct {
		domain.QueryHistory
		ConnectionID string `json:"connectionId"`
	}
	result := make([]entry, 0, len(items))
	for _, item := range items {
		result = append(result, entry{QueryHistory: item, ConnectionID: item.ConnectionID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": result})
}

func historyRange(r *http.Request) (from, to *string) {
	if value := strings.TrimSpace(r.URL.Query().Get("from")); value != "" {
		from = &value
	}
	if value := strings.TrimSpace(r.URL.Query().Get("to")); value != "" {
		to = &value
	}
	return from, to
}
