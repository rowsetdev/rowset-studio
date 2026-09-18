package api

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dbaopsio/rowset-studio/rowset-core/internal/domain"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/engine"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/id"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/policy"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/store"
	sqlguard "github.com/dbaopsio/rowset-studio/rowset-parser"
)

// cassandraBackupTargetOf is backupTargetOf's CQL counterpart: it recognizes
// UPDATE ks.table ... WHERE ... and DELETE FROM ks.table ... WHERE ... on one
// table. Unlike SQL, CQL never joins, so that check is dropped; unlike SQL,
// an UPDATE commonly carries a USING TTL/TIMESTAMP clause before SET, which
// this simply ignores (the WHERE clause, not USING, is what a restore needs).
// An IF clause (lightweight transactions) ends the captured WHERE, since IF
// is a condition on the write, not a predicate a SELECT understands.
func cassandraBackupTargetOf(info sqlguard.Info) (backupTarget, bool) {
	if (info.Command != sqlguard.Update && info.Command != sqlguard.Delete) || len(info.Tables) != 1 || !info.HasWhere {
		return backupTarget{}, false
	}
	var top []sqlguard.Token
	for _, token := range info.Tokens {
		if token.Depth == 0 {
			top = append(top, token)
		}
	}
	where := -1
	for index, token := range top {
		if token.Lower == "where" {
			where = index
			break
		}
	}
	if where < 0 {
		return backupTarget{}, false
	}
	end := len(info.Raw)
	for _, token := range top[where+1:] {
		if token.Lower == "if" {
			end = token.Start
			break
		}
	}
	clause := strings.TrimRight(strings.TrimSpace(info.Raw[top[where].End:end]), "; \t\r\n")
	if clause == "" {
		return backupTarget{}, false
	}
	kind := "update"
	if info.Command == sqlguard.Delete {
		kind = "delete"
	}
	table := info.Tables[0]
	return backupTarget{kind: kind, schema: table.Schema, table: table.Name, where: clause}, true
}

func cassandraQuotedIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func cassandraQuotedTable(keyspace, table string) string {
	return cassandraQuotedIdentifier(keyspace) + "." + cassandraQuotedIdentifier(table)
}

// cassandraLiteral is restoreLiteral's CQL counterpart. Kind alone decides
// the syntax: bool and num are written bare, uuid is written bare and
// unquoted (CQL's own literal form), bytes become a 0x-prefixed hex blob
// literal, and everything else (text, and timestamp's ISO8601 string) is a
// quoted string, which CQL accepts for timestamp/inet/date literals too.
func cassandraLiteral(value backupValue) string {
	switch value.Kind {
	case "null":
		return "null"
	case "bool", "num":
		return value.Value
	case "uuid":
		return value.Value
	case "bytes":
		raw, err := base64.StdEncoding.DecodeString(value.Value)
		if err != nil {
			return "0x"
		}
		return "0x" + hex.EncodeToString(raw)
	default: // "text", "time"
		return "'" + strings.ReplaceAll(value.Value, "'", "''") + "'"
	}
}

// cassandraRowBackupPayload mirrors rowBackupPayload's role for Cassandra:
// the rows a WHERE clause matched right before an UPDATE or DELETE, kept
// with enough metadata (primary key order, per-row values) to write them
// back. There is no generated/identity column concept in CQL, so it's just
// columns and rows.
type cassandraRowBackupPayload struct {
	BackupID   string          `json:"backupId"`
	UserID     string          `json:"userId"`
	Statement  string          `json:"statement"`
	Keyspace   string          `json:"keyspace"`
	Table      string          `json:"table"`
	PrimaryKey []string        `json:"primaryKey"`
	Columns    []string        `json:"columns"`
	Rows       [][]backupValue `json:"rows"`
}

// cassandraCaptureBackup saves the rows a simple UPDATE or DELETE is about
// to change, the same way captureRowBackup does for SQL. It returns nil when
// the statement isn't a simple single-table UPDATE/DELETE — nothing to back
// up, not an error.
func (s *Server) cassandraCaptureBackup(ctx context.Context, identity domain.Identity, connection domain.Connection, info sqlguard.Info, target engine.Connection, keyspace, statement string) Annotations {
	plan, ok := cassandraBackupTargetOf(info)
	if !ok || s.vault == nil {
		return nil
	}
	skipped := func(reason string) Annotations { return Annotations{"backupSkipped": reason} }
	ks := plan.schema
	if ks == "" {
		ks = keyspace
	}
	if ks == "" {
		return skipped("no keyspace selected")
	}
	tableInfo, err := s.engines.CassandraTableInfo(ctx, target, ks, plan.table)
	if err != nil {
		return skipped("table metadata could not be read: " + err.Error())
	}
	if tableInfo.IsCounter {
		return skipped("counter tables cannot be backed up")
	}
	if len(tableInfo.PrimaryKey) == 0 {
		return skipped(fmt.Sprintf("%s has no primary key information", plan.table))
	}
	for _, column := range tableInfo.Columns {
		if !column.Supported {
			return skipped(fmt.Sprintf("column %q has a type (%s) row backups don't support yet", column.Name, column.Type))
		}
	}
	selectCQL := "SELECT * FROM " + cassandraQuotedTable(ks, plan.table) + " WHERE " + plan.where
	columns, rows, truncated, err := s.engines.CassandraSelectRaw(ctx, target, ks, selectCQL, rowBackupLimit+1)
	if err != nil {
		return skipped("the changed rows could not be read: " + err.Error())
	}
	if len(rows) == 0 {
		return nil
	}
	if truncated {
		return Annotations{"backupBlocked": fmt.Sprintf("This %s changes more than %d rows, too many to back up.", strings.ToUpper(plan.kind), rowBackupLimit)}
	}
	payload := cassandraRowBackupPayload{BackupID: id.New(), UserID: identity.UserID, Statement: statement, Keyspace: ks, Table: plan.table, PrimaryKey: tableInfo.PrimaryKey, Columns: columns, Rows: make([][]backupValue, len(rows))}
	for index, row := range rows {
		payload.Rows[index] = make([]backupValue, len(row))
		for column, value := range row {
			payload.Rows[index][column] = backupValue{Kind: value.Kind, Value: value.Value}
		}
	}
	plain, err := json.Marshal(payload)
	if err != nil {
		return skipped("the backup could not be stored")
	}
	ciphertext, nonce, err := s.vault.Encrypt(plain)
	if err != nil {
		return skipped("the backup could not be stored")
	}
	item := store.RowBackup{ID: payload.BackupID, OrgID: identity.OrgID, UserID: identity.UserID, ConnectionID: connection.ID, Database: ks, Schema: "", Table: plan.table, Kind: plan.kind, Rows: int64(len(rows)), Ciphertext: ciphertext, Nonce: nonce, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := s.store.CreateRowBackup(context.Background(), item); err != nil {
		return skipped("the backup could not be stored")
	}
	return Annotations{"backup": map[string]any{"id": item.ID, "rows": item.Rows}}
}

func (s *Server) openCassandraRowBackup(item store.RowBackup) (cassandraRowBackupPayload, error) {
	var payload cassandraRowBackupPayload
	if s.vault == nil {
		return payload, errors.New("secret vault unavailable")
	}
	plain, err := s.vault.Decrypt(item.Ciphertext, item.Nonce)
	if err != nil {
		return payload, err
	}
	if err := json.Unmarshal(plain, &payload); err != nil || payload.BackupID != item.ID || payload.UserID != item.UserID {
		return payload, errors.New("the backup does not belong to this record")
	}
	return payload, nil
}

// cassandraRestoreStatements is restorePlan's CQL counterpart: a DELETE is
// undone with one INSERT per row (CQL has no multi-row INSERT), an UPDATE
// with one UPDATE per row that both restores every non-key column and pins
// the primary key in its WHERE clause. CQL has no server-side transaction to
// run them in, so the caller batches them with BEGIN BATCH/APPLY BATCH.
func cassandraRestoreStatements(item store.RowBackup, payload cassandraRowBackupPayload) []string {
	key := make(map[string]bool, len(payload.PrimaryKey))
	for _, name := range payload.PrimaryKey {
		key[strings.ToLower(name)] = true
	}
	index := func(name string) int {
		for i, column := range payload.Columns {
			if strings.EqualFold(column, name) {
				return i
			}
		}
		return -1
	}
	value := func(row []backupValue, i int) string {
		if i < 0 || i >= len(row) {
			return "null"
		}
		return cassandraLiteral(row[i])
	}
	table := cassandraQuotedTable(payload.Keyspace, payload.Table)
	statements := make([]string, 0, len(payload.Rows))
	if item.Kind == "delete" {
		for _, row := range payload.Rows {
			names := make([]string, len(payload.Columns))
			values := make([]string, len(payload.Columns))
			for i, column := range payload.Columns {
				names[i] = cassandraQuotedIdentifier(column)
				values[i] = value(row, i)
			}
			statements = append(statements, "INSERT INTO "+table+" ("+strings.Join(names, ", ")+") VALUES ("+strings.Join(values, ", ")+")")
		}
		return statements
	}
	for _, row := range payload.Rows {
		var assignments, conditions []string
		for _, column := range payload.Columns {
			i := index(column)
			if key[strings.ToLower(column)] {
				conditions = append(conditions, cassandraQuotedIdentifier(column)+" = "+value(row, i))
			} else {
				assignments = append(assignments, cassandraQuotedIdentifier(column)+" = "+value(row, i))
			}
		}
		if len(assignments) > 0 && len(conditions) > 0 {
			statements = append(statements, "UPDATE "+table+" SET "+strings.Join(assignments, ", ")+" WHERE "+strings.Join(conditions, " AND "))
		}
	}
	return statements
}

// cassandraRestoreBatches groups restore statements into CQL BEGIN
// BATCH/APPLY BATCH blocks small enough to stay under Cassandra's batch size
// warning threshold, which one statement per backed-up row could otherwise
// exceed for a large backup.
func cassandraRestoreBatches(statements []string) []string {
	const chunk = 50
	var batches []string
	for start := 0; start < len(statements); start += chunk {
		group := statements[start:min(start+chunk, len(statements))]
		var out strings.Builder
		out.WriteString("BEGIN BATCH\n")
		for _, statement := range group {
			out.WriteString("  " + statement + ";\n")
		}
		out.WriteString("APPLY BATCH")
		batches = append(batches, out.String())
	}
	return batches
}

// cassandraRestoreScript is restoreSQL's counterpart: real, runnable CQL
// (the editor's CQL path already executes BEGIN BATCH/APPLY BATCH as one
// governed statement), opened for review rather than applied directly.
func cassandraRestoreScript(item store.RowBackup, payload cassandraRowBackupPayload) string {
	table := cassandraQuotedTable(payload.Keyspace, payload.Table)
	batches := cassandraRestoreBatches(cassandraRestoreStatements(item, payload))
	var out strings.Builder
	fmt.Fprintf(&out, "-- Restores %d row(s) of %s backed up before this %s:\n", item.Rows, table, strings.ToUpper(item.Kind))
	for _, line := range strings.Split(strings.TrimSpace(payload.Statement), "\n") {
		out.WriteString("--   " + line + "\n")
	}
	out.WriteString("-- Review before running.\n\n")
	for _, batch := range batches {
		out.WriteString(batch + ";\n\n")
	}
	return out.String()
}

// applyCassandraRowBackup is applyRowBackup for a Cassandra backup: same
// auth and policy checks, but the restore runs as governed CQL batches
// instead of one SQL transaction (CQL has no equivalent to commit/rollback
// across statements).
func (s *Server) applyCassandraRowBackup(w http.ResponseWriter, r *http.Request, identity domain.Identity, connection domain.Connection, item store.RowBackup) {
	payload, err := s.openCassandraRowBackup(item)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the backup could not be read")
		return
	}
	statements := cassandraRestoreStatements(item, payload)
	if len(statements) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"rows": 0})
		return
	}
	batches := cassandraRestoreBatches(statements)
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
	var first sqlguard.Info
	for index, batch := range batches {
		info, err := sqlguard.ParseCQL(batch)
		if err != nil {
			writeError(w, http.StatusBadRequest, "PARSE_ERROR", err.Error())
			return
		}
		if index == 0 {
			first = info
		}
		decision := policy.Evaluate(policy.Input{Statement: info, Role: role.Name, ReadOnly: role.IsReadOnly || connection.ReadOnly, Environment: connection.Environment, Disabled: disabled, Enabled: enabled})
		if !s.config.Shared || !identity.IsAdmin() {
			if decision, _, err = s.applyCustomPolicies(r, identity, connection, info, false, decision, 0); err != nil {
				writeError(w, http.StatusInternalServerError, "INTERNAL", "governance rules unavailable")
				return
			}
		}
		if decision.Effect != policy.Allow {
			s.recordActivity(r, connection.ID, batch, "blocked", 0, 0, "", sqlguard.ExactHash(batch), auditMeta{decision: "deny", reason: decision.Reason, policyID: decision.PolicyID})
			writePolicyError(w, http.StatusForbidden, "POLICY_DENIED", decision, nil)
			return
		}
	}
	target, err := s.engineConnection(r, connection, payload.Keyspace)
	if err != nil {
		writeError(w, http.StatusBadGateway, "EXEC_ERROR", err.Error())
		return
	}
	ctx, cancel := withConnectionTimeout(r, connection, policyTimeout, 10*time.Minute)
	defer cancel()
	started := time.Now()
	for _, batch := range batches {
		if _, err := s.engines.CassandraQuery(ctx, target, engine.CassandraQueryInput{Keyspace: payload.Keyspace, Query: batch}); err != nil {
			duration := elapsedMilliseconds(started)
			s.recordActivity(r, connection.ID, batch, "error", 0, duration, "", sqlguard.ExactHash(batch), auditMeta{decision: "allow", errorMessage: err.Error()})
			writeError(w, http.StatusBadGateway, "EXEC_ERROR", err.Error())
			return
		}
	}
	duration := elapsedMilliseconds(started)
	statement := fmt.Sprintf("-- Row backup restore: %d row(s) of %s\n%s", item.Rows, cassandraQuotedTable(payload.Keyspace, payload.Table), first.Raw)
	s.recordActivity(r, connection.ID, statement, "success", item.Rows, duration, "", "", auditMeta{decision: "allow"})
	writeJSON(w, http.StatusOK, map[string]any{"rows": item.Rows})
}
