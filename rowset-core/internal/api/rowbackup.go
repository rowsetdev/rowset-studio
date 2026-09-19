package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/domain"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/engine"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/id"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/policy"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

// rowBackupLimit is the most rows an UPDATE or DELETE may change and still be
// backed up first.
const rowBackupLimit = 10_000

// backupTarget is the single table and WHERE clause of a simple UPDATE or
// DELETE.
type backupTarget struct {
	kind, schema, table, where string
}

// backupTargetOf recognizes UPDATE t SET … WHERE … and DELETE FROM t WHERE …
// on one table. Statements that join, use other tables or a WITH clause are
// not backed up, because the rows they change cannot be selected reliably.
func backupTargetOf(info sqlguard.Info) (backupTarget, bool) {
	if (info.Command != sqlguard.Update && info.Command != sqlguard.Delete) || len(info.Tables) != 1 || !info.HasWhere {
		return backupTarget{}, false
	}
	var top []sqlguard.Token
	for _, token := range info.Tokens {
		if token.Depth == 0 {
			top = append(top, token)
		}
	}
	if len(top) < 2 || top[0].Lower == "with" {
		return backupTarget{}, false
	}
	where, froms := -1, 0
	for index, token := range top {
		switch token.Lower {
		case "join", "using":
			return backupTarget{}, false
		case "from":
			froms++
		case "where":
			if where < 0 {
				where = index
			}
		}
	}
	if where < 0 || (info.Command == sqlguard.Update && froms > 0) || (info.Command == sqlguard.Delete && (froms != 1 || top[1].Lower != "from")) {
		return backupTarget{}, false
	}
	end := len(info.Raw)
	for _, token := range top[where+1:] {
		if token.Lower == "order" || token.Lower == "limit" || token.Lower == "returning" || token.Lower == "option" {
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

func qualifiedTable(engineName, schema, table string) string {
	name := quoteSQLIdentifier(engineName, table)
	if schema != "" {
		name = quoteSQLIdentifier(engineName, schema) + "." + name
	}
	return name
}

// primaryKeySQL lists the primary-key columns of a table in key order.
func primaryKeySQL(engineName, schema, table string) string {
	switch strings.ToLower(engineName) {
	case "mysql", "mariadb":
		schemaSQL := "DATABASE()"
		if schema != "" {
			schemaSQL = importLiteral(engineName, schema)
		}
		return "SELECT column_name FROM information_schema.key_column_usage WHERE constraint_name = 'PRIMARY' AND table_schema = " + schemaSQL + " AND table_name = " + importLiteral(engineName, table) + " ORDER BY ordinal_position"
	case "mssql", "sqlserver":
		return "SELECT c.name FROM sys.indexes i JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id WHERE i.is_primary_key = 1 AND i.object_id = OBJECT_ID(" + importLiteral(engineName, qualifiedTable(engineName, schema, table)) + ") ORDER BY ic.key_ordinal"
	default:
		return "SELECT a.attname FROM pg_index i JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey) WHERE i.indisprimary AND i.indrelid = to_regclass(" + importLiteral(engineName, qualifiedTable(engineName, schema, table)) + ") ORDER BY array_position(i.indkey::int2[], a.attnum)"
	}
}

// columnKindsSQL lists the columns a restore must treat specially: generated
// and computed columns (and SQL Server rowversion), which are never written,
// and identity columns, which need an override to take an explicit value.
func columnKindsSQL(engineName, schema, table string) string {
	switch strings.ToLower(engineName) {
	case "mysql", "mariadb":
		schemaSQL := "DATABASE()"
		if schema != "" {
			schemaSQL = importLiteral(engineName, schema)
		}
		return "SELECT column_name, 'generated' FROM information_schema.columns WHERE table_schema = " + schemaSQL + " AND table_name = " + importLiteral(engineName, table) + " AND extra IN ('STORED GENERATED', 'VIRTUAL GENERATED')"
	case "mssql", "sqlserver":
		return "SELECT c.name, CASE WHEN c.is_computed = 1 OR t.name = 'timestamp' THEN 'generated' ELSE 'identity' END FROM sys.columns c JOIN sys.types t ON t.user_type_id = c.user_type_id WHERE c.object_id = OBJECT_ID(" + importLiteral(engineName, qualifiedTable(engineName, schema, table)) + ") AND (c.is_identity = 1 OR c.is_computed = 1 OR t.name = 'timestamp')"
	default:
		return "SELECT attname, CASE WHEN attgenerated <> '' THEN 'generated' WHEN attidentity = 'a' THEN 'identity_always' ELSE 'identity' END FROM pg_attribute WHERE attrelid = to_regclass(" + importLiteral(engineName, qualifiedTable(engineName, schema, table)) + ") AND attnum > 0 AND NOT attisdropped AND (attgenerated <> '' OR attidentity <> '')"
	}
}

// readRows runs a read on the pinned transaction or a pooled connection.
func (s *Server) readRows(ctx context.Context, target engine.Connection, transaction *engine.Transaction, query string, max int) ([]string, []string, [][]any, error) {
	var stream *engine.RowStream
	var err error
	if transaction != nil {
		stream, err = transaction.Query(ctx, query)
	} else {
		stream, err = s.engines.Query(ctx, target, query)
	}
	if err != nil {
		return nil, nil, nil, err
	}
	defer stream.Close()
	var rows [][]any
	for len(rows) < max {
		row, ok, err := stream.NextRaw()
		if err != nil {
			return nil, nil, nil, err
		}
		if !ok {
			break
		}
		rows = append(rows, row)
	}
	return stream.Columns(), stream.DatabaseTypes(), rows, nil
}

// backupValue keeps a value's kind so the restore script can write it back.
type backupValue struct {
	Kind  string `json:"t"`
	Value string `json:"v,omitempty"`
}

// isBinaryType reports whether a database type holds raw bytes, which are
// written back as hex even when they happen to be valid text.
func isBinaryType(databaseType string) bool {
	databaseType = strings.ToUpper(databaseType)
	for _, kind := range []string{"BINARY", "BYTEA", "BLOB", "IMAGE", "UNIQUEIDENTIFIER", "BIT", "GEOMETRY", "GEOGRAPHY", "HIERARCHYID"} {
		if strings.Contains(databaseType, kind) {
			return true
		}
	}
	return false
}

func encodeBackupValue(value any, databaseType string) backupValue {
	switch v := value.(type) {
	case nil:
		return backupValue{Kind: "null"}
	case bool:
		return backupValue{Kind: "bool", Value: strconv.FormatBool(v)}
	case int64:
		return backupValue{Kind: "num", Value: strconv.FormatInt(v, 10)}
	case int32:
		return backupValue{Kind: "num", Value: strconv.FormatInt(int64(v), 10)}
	case int:
		return backupValue{Kind: "num", Value: strconv.Itoa(v)}
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return backupValue{Kind: "text", Value: specialFloatName(v)}
		}
		return backupValue{Kind: "num", Value: strconv.FormatFloat(v, 'g', -1, 64)}
	case float32:
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return backupValue{Kind: "text", Value: specialFloatName(float64(v))}
		}
		return backupValue{Kind: "num", Value: strconv.FormatFloat(float64(v), 'g', -1, 32)}
	case time.Time:
		// Zone-aware types keep the offset; the others take the wall-clock
		// value, which every engine reads back into the same column type.
		if databaseType == "" {
			return backupValue{Kind: "time", Value: v.Format("2006-01-02 15:04:05.999999999")}
		}
		return backupValue{Kind: "time", Value: engine.FormatTime(v, databaseType)}
	case []byte:
		if !isBinaryType(databaseType) && utf8.Valid(v) {
			return backupValue{Kind: "text", Value: string(v)}
		}
		return backupValue{Kind: "bytes", Value: hex.EncodeToString(v)}
	case string:
		return backupValue{Kind: "text", Value: v}
	default:
		return backupValue{Kind: "text", Value: fmt.Sprint(v)}
	}
}

// specialFloatName is how PostgreSQL spells NaN and the infinities.
func specialFloatName(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	default:
		return "-Infinity"
	}
}

func restoreLiteral(engineName string, value backupValue) string {
	engineName = strings.ToLower(engineName)
	switch value.Kind {
	case "null":
		return "NULL"
	case "bool":
		if engineName == "postgres" {
			return strings.ToUpper(value.Value)
		}
		if value.Value == "true" {
			return "1"
		}
		return "0"
	case "num":
		return value.Value
	case "bytes":
		switch engineName {
		case "mysql", "mariadb":
			return "X'" + value.Value + "'"
		case "mssql", "sqlserver":
			return "0x" + value.Value
		default:
			return `'\x` + value.Value + `'::bytea`
		}
	default:
		return importLiteral(engineName, value.Value)
	}
}

// rowBackupPayload is encrypted; the envelope binds it to its record.
type rowBackupPayload struct {
	BackupID  string          `json:"backupId"`
	UserID    string          `json:"userId"`
	Statement string          `json:"statement"`
	Columns   []string        `json:"columns"`
	Key       []string        `json:"key"`
	Rows      [][]backupValue `json:"rows"`
	// Generated columns are derived by the database and never written back.
	Generated []string `json:"generated,omitempty"`
	// Identity columns need OVERRIDING SYSTEM VALUE (PostgreSQL, when
	// IdentityAlways) or IDENTITY_INSERT (SQL Server) to take their old value.
	Identity       []string `json:"identity,omitempty"`
	IdentityAlways bool     `json:"identityAlways,omitempty"`
	// Types are the database types of Columns, as the driver names them.
	Types []string `json:"types,omitempty"`
}

// captureRowBackup saves the rows a simple UPDATE or DELETE is about to change
// and returns response fields describing the backup, or why none was taken.
// "backupBlocked" means the statement must not run until the user agrees to
// run it without a backup.
// Inside a PostgreSQL transaction the read runs under a savepoint, so a
// failed backup never aborts the caller's transaction.
func (s *Server) captureRowBackup(ctx context.Context, identity domain.Identity, connection domain.Connection, info sqlguard.Info, target engine.Connection, transaction *engine.Transaction, database string) Annotations {
	plan, ok := backupTargetOf(info)
	if !ok || s.vault == nil {
		return nil
	}
	skipped := func(reason string) Annotations { return Annotations{"backupSkipped": reason} }
	table := qualifiedTable(connection.Engine, plan.schema, plan.table)
	selectSQL := "SELECT * FROM " + table + " WHERE " + plan.where
	if _, err := sqlguard.Parse(selectSQL); err != nil {
		return skipped("the changed rows could not be selected")
	}
	savepoint := transaction != nil && connection.Engine == "postgres"
	if savepoint {
		if _, err := transaction.Execute(ctx, "SAVEPOINT rowset_backup", 0); err != nil {
			return skipped("the transaction does not allow a backup")
		}
	}
	columns, types, rows, readErr := s.readRows(ctx, target, transaction, selectSQL, rowBackupLimit+1)
	var keyRows, kindRows [][]any
	if readErr == nil {
		_, _, keyRows, readErr = s.readRows(ctx, target, transaction, primaryKeySQL(connection.Engine, plan.schema, plan.table), 64)
	}
	if readErr == nil {
		_, _, kindRows, readErr = s.readRows(ctx, target, transaction, columnKindsSQL(connection.Engine, plan.schema, plan.table), 4096)
	}
	if savepoint {
		release := "RELEASE SAVEPOINT rowset_backup"
		if readErr != nil {
			release = "ROLLBACK TO SAVEPOINT rowset_backup"
		}
		_, _ = transaction.Execute(ctx, release, 0)
	}
	if readErr != nil {
		return skipped("the changed rows could not be read: " + readErr.Error())
	}
	if len(rows) == 0 {
		return nil
	}
	blocked := func(reason string) Annotations { return Annotations{"backupBlocked": reason} }
	if len(rows) > rowBackupLimit {
		return blocked(fmt.Sprintf("This %s changes more than %d rows, too many to back up.", strings.ToUpper(plan.kind), rowBackupLimit))
	}
	key := make([]string, 0, len(keyRows))
	for _, row := range keyRows {
		if len(row) > 0 {
			key = append(key, encodeBackupValue(row[0], "").Value)
		}
	}
	if plan.kind == "update" && len(key) == 0 {
		return blocked(fmt.Sprintf("%s has no primary key, so the old values could not be put back.", plan.table))
	}
	// Leading comments are left out of the saved statement.
	statement := info.Raw
	if len(info.Tokens) > 0 {
		statement = strings.TrimSpace(info.Raw[info.Tokens[0].Start:])
	}
	payload := rowBackupPayload{BackupID: id.New(), UserID: identity.UserID, Statement: statement, Columns: columns, Types: types, Key: key, Rows: make([][]backupValue, len(rows))}
	for _, row := range kindRows {
		if len(row) < 2 {
			continue
		}
		name, kind := encodeBackupValue(row[0], "").Value, encodeBackupValue(row[1], "").Value
		switch kind {
		case "generated":
			payload.Generated = append(payload.Generated, name)
		case "identity_always":
			payload.IdentityAlways = true
			payload.Identity = append(payload.Identity, name)
		default:
			payload.Identity = append(payload.Identity, name)
		}
	}
	for index, row := range rows {
		payload.Rows[index] = make([]backupValue, len(row))
		for column, value := range row {
			databaseType := ""
			if column < len(types) {
				databaseType = types[column]
			}
			payload.Rows[index][column] = encodeBackupValue(value, databaseType)
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
	item := store.RowBackup{ID: payload.BackupID, OrgID: identity.OrgID, UserID: identity.UserID, ConnectionID: connection.ID, Database: database, Schema: plan.schema, Table: plan.table, Kind: plan.kind, Rows: int64(len(rows)), Ciphertext: ciphertext, Nonce: nonce, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := s.store.CreateRowBackup(context.Background(), item); err != nil {
		return skipped("the backup could not be stored")
	}
	return Annotations{"backup": map[string]any{"id": item.ID, "rows": item.Rows}}
}

// discardRowBackup removes the backup of a statement that failed, since it
// changed nothing.
func (s *Server) discardRowBackup(identity domain.Identity, annotations Annotations) {
	if backup, ok := annotations["backup"].(map[string]any); ok {
		if backupID, ok := backup["id"].(string); ok {
			_ = s.store.DeleteRowBackup(context.Background(), backupID, identity.UserID)
		}
	}
}

func (s *Server) openRowBackup(item store.RowBackup) (rowBackupPayload, error) {
	var payload rowBackupPayload
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

func nameSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[strings.ToLower(name)] = true
	}
	return set
}

// restorePlan returns the statements that put the backed-up rows back: the
// old values for an UPDATE, the deleted rows for a DELETE. identityInsert
// means SQL Server must allow explicit identity values while they run.
// For an UPDATE backup, probes[i] selects the row statements[i] restores, so
// a restore that matches no row can tell a row whose values are already
// restored from one that no longer exists under its backed-up key.
func restorePlan(engineName string, item store.RowBackup, payload rowBackupPayload) (statements, probes []string, identityInsert bool) {
	table := qualifiedTable(engineName, item.Schema, item.Table)
	generated, identity, key := nameSet(payload.Generated), nameSet(payload.Identity), nameSet(payload.Key)
	var writable []int
	for index, column := range payload.Columns {
		if !generated[strings.ToLower(column)] {
			writable = append(writable, index)
		}
	}
	quoted := func(index int) string { return quoteSQLIdentifier(engineName, payload.Columns[index]) }
	sqlServer := strings.EqualFold(engineName, "mssql") || strings.EqualFold(engineName, "sqlserver")
	value := func(row []backupValue, index int) string {
		if index >= len(row) {
			return "NULL"
		}
		literal := restoreLiteral(engineName, row[index])
		// A multi-row VALUES list gives each column one type, which would turn
		// every sql_variant value into the first row's type.
		if sqlServer && row[index].Kind != "null" && index < len(payload.Types) && payload.Types[index] == "SQL_VARIANT" {
			return "CAST(" + literal + " AS sql_variant)"
		}
		return literal
	}
	if item.Kind == "delete" {
		names := make([]string, len(writable))
		usesIdentity := false
		for position, index := range writable {
			names[position] = quoted(index)
			usesIdentity = usesIdentity || identity[strings.ToLower(payload.Columns[index])]
		}
		prefix := "INSERT INTO " + table + " (" + strings.Join(names, ", ") + ")"
		if usesIdentity && payload.IdentityAlways && strings.EqualFold(engineName, "postgres") {
			prefix += " OVERRIDING SYSTEM VALUE"
		}
		identityInsert = usesIdentity && (strings.EqualFold(engineName, "mssql") || strings.EqualFold(engineName, "sqlserver"))
		for start := 0; start < len(payload.Rows); start += 100 {
			var statement strings.Builder
			statement.WriteString(prefix + " VALUES\n")
			for offset, row := range payload.Rows[start:min(start+100, len(payload.Rows))] {
				if offset > 0 {
					statement.WriteString(",\n")
				}
				values := make([]string, len(writable))
				for position, index := range writable {
					values[position] = value(row, index)
				}
				statement.WriteString("  (" + strings.Join(values, ", ") + ")")
			}
			statements = append(statements, statement.String())
		}
		return statements, nil, identityInsert
	}
	for _, row := range payload.Rows {
		var assignments, conditions []string
		for _, index := range writable {
			name := strings.ToLower(payload.Columns[index])
			switch {
			case key[name]:
				if index < len(row) && row[index].Kind == "null" {
					conditions = append(conditions, quoted(index)+" IS NULL")
				} else {
					conditions = append(conditions, quoted(index)+" = "+value(row, index))
				}
			case !identity[name]:
				assignments = append(assignments, quoted(index)+" = "+value(row, index))
			}
		}
		if len(assignments) > 0 && len(conditions) > 0 {
			where := " WHERE " + strings.Join(conditions, " AND ")
			statements = append(statements, "UPDATE "+table+" SET "+strings.Join(assignments, ", ")+where)
			probes = append(probes, "SELECT 1 FROM "+table+where)
		}
	}
	return statements, probes, false
}

// restoreSQL is the restore as a script to review in the editor.
func restoreSQL(engineName string, item store.RowBackup, payload rowBackupPayload) string {
	table := qualifiedTable(engineName, item.Schema, item.Table)
	statements, _, identityInsert := restorePlan(engineName, item, payload)
	var out strings.Builder
	fmt.Fprintf(&out, "-- Restores %d row(s) of %s backed up before this %s:\n", item.Rows, table, strings.ToUpper(item.Kind))
	for _, line := range strings.Split(strings.TrimSpace(payload.Statement), "\n") {
		out.WriteString("--   " + line + "\n")
	}
	if identityInsert {
		out.WriteString("-- " + table + " has an identity column, so these INSERTs need IDENTITY_INSERT, which the editor cannot turn on.\n-- Use Restore in Activity → Row backups instead; it runs them in one transaction.\n")
	}
	out.WriteString("-- Review before running. In manual commit mode nothing is saved until you press Commit.\n\n")
	for _, statement := range statements {
		out.WriteString(statement + ";\n")
	}
	return out.String()
}

// applyRowBackup puts the backed-up rows back in one transaction. Each
// statement passes the same policies as in the editor.
func (s *Server) applyRowBackup(w http.ResponseWriter, r *http.Request) {
	defer s.holdAwake()()
	identity := identityFromContext(r.Context())
	item, err := s.store.RowBackup(r.Context(), r.PathValue("id"), identity.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "row backup not found")
		return
	}
	connection, err := s.store.Connection(r.Context(), item.ConnectionID)
	if err != nil || connection.OrgID != identity.OrgID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "the connection no longer exists")
		return
	}
	role, err := s.store.UserRole(r.Context(), identity.UserID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "role missing")
		return
	}
	if !s.canUseConnection(r, identity, role.ID, connection) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "you no longer have access to this connection")
		return
	}
	// The stored payload's shape is engine-specific (rowBackupPayload's SQL
	// columns/rows vs mongoBackupPayload's whole documents), so which decoder
	// runs must be chosen before either one touches the ciphertext.
	if connection.Engine == "mongodb" {
		s.applyMongoRowBackup(w, r, identity, connection, item)
		return
	}
	if connection.Engine == "cassandra" {
		s.applyCassandraRowBackup(w, r, identity, connection, item)
		return
	}
	if connection.Engine == "redis" || connection.Engine == "valkey" {
		s.applyRedisRowBackup(w, r, identity, connection, item)
		return
	}
	if connection.Engine == "elasticsearch" {
		s.applyElasticsearchRowBackup(w, r, identity, connection, item)
		return
	}
	payload, err := s.openRowBackup(item)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the backup could not be read")
		return
	}
	statements, probes, identityInsert := restorePlan(connection.Engine, item, payload)
	if len(statements) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"rows": 0})
		return
	}
	disabled, enabled, _, policyTimeout, err := s.resolvePolicies(r, identity, connection)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "governance rules unavailable")
		return
	}
	var first sqlguard.Info
	for index, statement := range statements {
		info, err := sqlguard.Parse(statement)
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
			normalized, hash := sqlguard.Normalize(info)
			s.recordActivity(r, connection.ID, statement, "blocked", 0, 0, normalized, hash, auditMeta{decision: "deny", reason: decision.Reason, policyID: decision.PolicyID})
			writePolicyError(w, http.StatusForbidden, "POLICY_DENIED", decision, nil)
			return
		}
	}
	primary := "primary"
	target, _, err := s.routedEngineConnection(r.Context(), identity, connection, item.Database, &primary, &first)
	if err != nil {
		writeError(w, http.StatusBadGateway, "EXEC_ERROR", err.Error())
		return
	}
	ctx, cancel := withConnectionTimeout(r, connection, policyTimeout, 10*time.Minute)
	defer cancel()
	started := time.Now()
	transaction, err := s.engines.Begin(ctx, target)
	if err != nil {
		writeError(w, http.StatusBadGateway, "EXEC_ERROR", err.Error())
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	table := qualifiedTable(connection.Engine, item.Schema, item.Table)
	if identityInsert {
		statements = append(append([]string{"SET IDENTITY_INSERT " + table + " ON"}, statements...), "SET IDENTITY_INSERT "+table+" OFF")
	}
	for index, statement := range statements {
		result, err := transaction.Execute(ctx, statement, 0)
		if err != nil {
			writeStatementError(w, err, statement, nil)
			return
		}
		// MySQL counts only changed rows, so a zero count is checked against
		// the row itself before the restore is refused.
		if index < len(probes) && result.RowsAffected == 0 {
			found, err := transaction.Execute(ctx, probes[index], 1)
			if err != nil {
				writeStatementError(w, err, probes[index], nil)
				return
			}
			if len(found.Rows) == 0 {
				writeError(w, http.StatusConflict, "RESTORE_ROW_MISSING", "a backed-up row no longer exists under its original key (it was deleted, or the update changed its key); nothing was restored. Open the Script to restore it by hand.")
				return
			}
		}
	}
	if err := transaction.Commit(); err != nil {
		writeError(w, http.StatusBadGateway, "EXEC_ERROR", "the restore could not be committed: "+err.Error())
		return
	}
	committed = true
	normalized, hash := sqlguard.Normalize(first)
	s.recordActivity(r, connection.ID, fmt.Sprintf("-- Row backup restore: %d row(s) of %s\n%s", item.Rows, table, first.Raw), "success", item.Rows, elapsedMilliseconds(started), normalized, hash, auditMeta{decision: "allow", command: true})
	writeJSON(w, http.StatusOK, map[string]any{"rows": item.Rows})
}

func (s *Server) listRowBackups(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListRowBackups(r.Context(), identityFromContext(r.Context()).UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "row backups unavailable")
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		statement := ""
		if payload, err := s.openRowBackup(item); err == nil {
			statement = payload.Statement
		}
		out = append(out, map[string]any{"id": item.ID, "connectionId": item.ConnectionID, "database": item.Database, "schema": item.Schema, "table": item.Table, "kind": item.Kind, "rows": item.Rows, "statement": statement, "createdAt": item.CreatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": out})
}

func (s *Server) rowBackupRestore(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.RowBackup(r.Context(), r.PathValue("id"), identityFromContext(r.Context()).UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "row backup not found")
		return
	}
	connection, err := s.store.Connection(r.Context(), item.ConnectionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "the connection no longer exists")
		return
	}
	if connection.Engine == "mongodb" {
		payload, err := s.openMongoRowBackup(item)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL", "the backup could not be read")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sql": mongoRestoreScript(item, payload), "connectionId": item.ConnectionID, "database": item.Database, "table": item.Table})
		return
	}
	if connection.Engine == "cassandra" {
		payload, err := s.openCassandraRowBackup(item)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL", "the backup could not be read")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sql": cassandraRestoreScript(item, payload), "connectionId": item.ConnectionID, "database": item.Database, "table": item.Table})
		return
	}
	if connection.Engine == "redis" || connection.Engine == "valkey" {
		payload, err := s.openRedisRowBackup(item)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL", "the backup could not be read")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sql": redisRestoreScript(item, payload), "connectionId": item.ConnectionID, "database": item.Database, "table": item.Table})
		return
	}
	if connection.Engine == "elasticsearch" {
		payload, err := s.openElasticsearchRowBackup(item)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL", "the backup could not be read")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sql": elasticsearchRestoreScript(item, payload), "connectionId": item.ConnectionID, "database": item.Database, "table": item.Table})
		return
	}
	payload, err := s.openRowBackup(item)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the backup could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sql": restoreSQL(connection.Engine, item, payload), "connectionId": item.ConnectionID, "database": item.Database, "table": item.Table})
}

func (s *Server) deleteRowBackup(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteRowBackup(r.Context(), r.PathValue("id"), identityFromContext(r.Context()).UserID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
