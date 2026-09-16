package api

import (
	"bufio"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dbaopsio/rowset-studio/rowset-core/internal/domain"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/engine"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/id"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/policy"
	sqlguard "github.com/dbaopsio/rowset-studio/rowset-parser"
)

const (
	importMaxBytes           = 256 << 20
	importBatchRows          = 500
	cassandraImportBatchRows = 50
	importBatchBytes         = 512 << 10
	importUploadMaxAge       = time.Hour
)

// csvUpload is a CSV file being uploaded in chunks before it is imported in
// one transaction.
type csvUpload struct {
	userID, connectionID, path string
	size                       int64
	created                    time.Time
}

type importColumn struct {
	Source int    `json:"source"`
	Target string `json:"target"`
}

type importInput struct {
	Schema    string         `json:"schema"`
	Table     string         `json:"table"`
	Database  string         `json:"database"`
	Header    bool           `json:"header"`
	Delimiter string         `json:"delimiter"`
	NullEmpty bool           `json:"nullEmpty"`
	Columns   []importColumn `json:"columns"`
}

func importDir() string { return filepath.Join(os.TempDir(), "rowset-imports") }

func (s *Server) upload(r *http.Request) (*csvUpload, bool) {
	identity := identityFromContext(r.Context())
	s.importMu.Lock()
	defer s.importMu.Unlock()
	item, ok := s.imports[r.PathValue("importId")]
	return item, ok && item.userID == identity.UserID && item.connectionID == r.PathValue("id")
}

func (s *Server) dropUpload(importID string) {
	s.importMu.Lock()
	item := s.imports[importID]
	delete(s.imports, importID)
	s.importMu.Unlock()
	if item != nil {
		_ = os.Remove(item.path)
	}
}

// startImport opens an upload; files not imported within an hour are removed.
func (s *Server) startImport(w http.ResponseWriter, r *http.Request) {
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	if engine.AdditionalEngine(connection.Engine) && connection.Engine != "cassandra" {
		writeError(w, 400, "UNSUPPORTED", "CSV import is not available for this engine yet")
		return
	}
	s.importMu.Lock()
	for key, item := range s.imports {
		if time.Since(item.created) > importUploadMaxAge {
			_ = os.Remove(item.path)
			delete(s.imports, key)
		}
	}
	s.importMu.Unlock()
	if err := os.MkdirAll(importDir(), 0o700); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "import folder unavailable")
		return
	}
	file, err := os.CreateTemp(importDir(), "*.csv")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "import file unavailable")
		return
	}
	_ = file.Close()
	importID := id.New()
	s.importMu.Lock()
	s.imports[importID] = &csvUpload{userID: identityFromContext(r.Context()).UserID, connectionID: connection.ID, path: file.Name(), created: time.Now()}
	s.importMu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"importId": importID})
}

// appendImport adds the request body to the upload.
func (s *Server) appendImport(w http.ResponseWriter, r *http.Request) {
	item, ok := s.upload(r)
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "import not found")
		return
	}
	file, err := os.OpenFile(item.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "import file unavailable")
		return
	}
	written, err := io.Copy(file, io.LimitReader(r.Body, importMaxBytes-item.size+1))
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "the chunk could not be stored")
		return
	}
	s.importMu.Lock()
	item.size += written
	size := item.size
	s.importMu.Unlock()
	if size > importMaxBytes {
		s.dropUpload(r.PathValue("importId"))
		writeError(w, http.StatusRequestEntityTooLarge, "TOO_LARGE", "CSV files are limited to 256 MB")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"size": size})
}

func (s *Server) discardImport(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.upload(r); ok {
		s.dropUpload(r.PathValue("importId"))
	}
	w.WriteHeader(http.StatusNoContent)
}

// runImport inserts the uploaded rows into an existing table in one
// transaction: either every row is imported or none is.
func (s *Server) runImport(w http.ResponseWriter, r *http.Request) {
	defer s.holdAwake()()
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	item, ok := s.upload(r)
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "import not found")
		return
	}
	defer s.dropUpload(r.PathValue("importId"))
	var input importInput
	if !decodeJSON(w, r, &input) {
		return
	}
	delimiter, err := importDelimiter(input.Delimiter)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	input.Schema, input.Table = strings.TrimSpace(input.Schema), strings.TrimSpace(input.Table)
	if input.Table == "" || len(input.Columns) == 0 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "choose a table and at least one column")
		return
	}
	seen := map[string]bool{}
	names := make([]string, 0, len(input.Columns))
	for _, column := range input.Columns {
		key := strings.ToLower(strings.TrimSpace(column.Target))
		if key == "" || column.Source < 0 || seen[key] {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "each table column can be filled from one CSV column")
			return
		}
		seen[key] = true
		names = append(names, quoteSQLIdentifier(connection.Engine, strings.TrimSpace(column.Target)))
	}
	table := quoteSQLIdentifier(connection.Engine, input.Table)
	if input.Schema != "" {
		table = quoteSQLIdentifier(connection.Engine, input.Schema) + "." + table
	}
	prefix := "INSERT INTO " + table + " (" + strings.Join(names, ", ") + ") VALUES "
	shape := prefix + "(" + strings.TrimSuffix(strings.Repeat("NULL, ", len(names)), ", ") + ")"
	info, err := sqlguard.Parse(shape)
	if err != nil {
		writeError(w, http.StatusBadRequest, "PARSE_ERROR", err.Error())
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
	normalized, hash := sqlguard.Normalize(info)
	decision := policy.Evaluate(policy.Input{Statement: info, Role: role.Name, ReadOnly: role.IsReadOnly || connection.ReadOnly, Environment: connection.Environment, Disabled: disabled, Enabled: enabled})
	if !s.config.Shared || !identity.IsAdmin() {
		if decision, _, err = s.applyCustomPolicies(r, identity, connection, info, false, decision, 0); err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL", "governance rules unavailable")
			return
		}
	}
	if decision.Effect != policy.Allow {
		s.recordActivity(r, connection.ID, shape, "blocked", 0, 0, normalized, hash, auditMeta{decision: "deny", reason: decision.Reason, policyID: decision.PolicyID})
		writePolicyError(w, http.StatusForbidden, "POLICY_DENIED", decision, nil)
		return
	}
	if connection.Engine == "cassandra" {
		s.runCassandraImport(w, r, connection, input, item, policyTimeout, shape, hash)
		return
	}
	primary := "primary"
	target, _, err := s.routedEngineConnection(r.Context(), identity, connection, strings.TrimSpace(input.Database), &primary, &info)
	if err != nil {
		writeError(w, http.StatusBadGateway, "EXEC_ERROR", err.Error())
		return
	}
	// Binary columns take exported \x-prefixed hex as bytes, not as text.
	hexColumns := map[int]bool{}
	_, _, typeRows, typeErr := s.readRows(r.Context(), target, nil, importColumnTypesSQL(connection.Engine, input.Schema, input.Table), 4096)
	if typeErr != nil {
		writeError(w, http.StatusBadGateway, "EXEC_ERROR", "the column types of the table could not be read: "+typeErr.Error())
		return
	}
	{
		binary := map[string]bool{}
		for _, row := range typeRows {
			if len(row) >= 2 {
				binary[strings.ToLower(encodeBackupValue(row[0], "").Value)] = importBinaryType(encodeBackupValue(row[1], "").Value)
			}
		}
		for index, column := range input.Columns {
			hexColumns[index] = binary[strings.ToLower(strings.TrimSpace(column.Target))]
		}
	}
	file, err := os.Open(item.path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "import file unavailable")
		return
	}
	defer file.Close()
	ctx, cancel := withConnectionTimeout(r, connection, policyTimeout, 30*time.Minute)
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
	reader := csv.NewReader(bufio.NewReader(file))
	reader.Comma, reader.FieldsPerRecord, reader.LazyQuotes, reader.ReuseRecord = delimiter, -1, true, true
	var rows int64
	var batch strings.Builder
	batchRows := 0
	flush := func() error {
		if batchRows == 0 {
			return nil
		}
		_, err := transaction.Execute(ctx, batch.String(), 0)
		batch.Reset()
		batchRows = 0
		return err
	}
	line := 0
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_CSV", fmt.Sprintf("line %d: %v", line, err))
			return
		}
		if line == 1 && len(record) > 0 {
			record[0] = strings.TrimPrefix(record[0], "\ufeff")
		}
		if line == 1 && input.Header {
			continue
		}
		if len(record) == 1 && record[0] == "" {
			continue
		}
		if batchRows == 0 {
			batch.WriteString(prefix)
		} else {
			batch.WriteString(", ")
		}
		batch.WriteByte('(')
		for index, column := range input.Columns {
			if index > 0 {
				batch.WriteString(", ")
			}
			value := ""
			if column.Source < len(record) {
				value = record[column.Source]
			}
			if value == "" && input.NullEmpty {
				batch.WriteString("NULL")
			} else if hexColumns[index] && hexText(value) {
				batch.WriteString(hexLiteral(connection.Engine, value[2:]))
			} else {
				batch.WriteString(importLiteral(connection.Engine, value))
			}
		}
		batch.WriteByte(')')
		batchRows++
		rows++
		if batchRows >= importBatchRows || batch.Len() >= importBatchBytes {
			if err := flush(); err != nil {
				s.recordActivity(r, connection.ID, shape, "error", 0, elapsedMilliseconds(started), normalized, hash, auditMeta{decision: "allow", errorMessage: err.Error(), command: true})
				writeStatementError(w, fmt.Errorf("rows up to CSV line %d: %w", line, err), shape, nil)
				return
			}
		}
	}
	if err := flush(); err == nil {
		err = transaction.Commit()
		if err == nil {
			committed = true
		}
	} else {
		s.recordActivity(r, connection.ID, shape, "error", 0, elapsedMilliseconds(started), normalized, hash, auditMeta{decision: "allow", errorMessage: err.Error(), command: true})
		writeStatementError(w, fmt.Errorf("rows up to CSV line %d: %w", line, err), shape, nil)
		return
	}
	if !committed {
		writeError(w, http.StatusBadGateway, "EXEC_ERROR", "the import could not be committed")
		return
	}
	duration := elapsedMilliseconds(started)
	s.recordActivity(r, connection.ID, fmt.Sprintf("-- CSV import: %d rows\n%s", rows, shape), "success", rows, duration, normalized, hash, auditMeta{decision: "allow", command: true})
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "durationMs": duration})
}

func (s *Server) runCassandraImport(w http.ResponseWriter, r *http.Request, connection domain.Connection, input importInput, item *csvUpload, timeout int, statement, hash string) {
	delimiter, _ := importDelimiter(input.Delimiter)
	file, err := os.Open(item.path)
	if err != nil {
		writeError(w, 500, "INTERNAL", "import file unavailable")
		return
	}
	defer file.Close()
	keyspace := input.Schema
	if keyspace == "" {
		keyspace = input.Database
	}
	if keyspace == "" {
		keyspace = connection.Database
	}
	target, err := s.engineConnection(r, connection, keyspace)
	if err != nil {
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	columns := make([]string, len(input.Columns))
	for i, column := range input.Columns {
		columns[i] = strings.TrimSpace(column.Target)
	}
	reader := csv.NewReader(bufio.NewReader(file))
	reader.Comma, reader.FieldsPerRecord, reader.LazyQuotes = delimiter, -1, true
	ctx, cancel := withConnectionTimeout(r, connection, timeout, 30*time.Minute)
	defer cancel()
	started, rows := time.Now(), int64(0)
	first := true
	batch := make([][]any, 0, cassandraImportBatchRows)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := s.engines.CassandraImport(ctx, target, keyspace, input.Table, columns, batch)
		batch = batch[:0]
		return err
	}
	for {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			err = readErr
			break
		}
		if first && input.Header {
			first = false
			continue
		}
		first = false
		values := make([]any, len(input.Columns))
		valid := true
		for i, column := range input.Columns {
			if column.Source >= len(record) {
				valid = false
				break
			}
			value := record[column.Source]
			if input.NullEmpty && value == "" {
				values[i] = nil
			} else {
				values[i] = value
			}
		}
		if !valid {
			err = fmt.Errorf("CSV row %d has too few columns", rows+1)
			break
		}
		batch = append(batch, values)
		rows++
		if len(batch) >= cassandraImportBatchRows {
			if err = flush(); err != nil {
				break
			}
		}
	}
	if err == nil {
		err = flush()
	}
	duration := time.Since(started).Milliseconds()
	if err != nil {
		s.recordActivity(r, connection.ID, statement, "error", rows, duration, "", hash, auditMeta{decision: "allow", errorMessage: err.Error()})
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	s.recordActivity(r, connection.ID, statement, "success", rows, duration, "", hash, auditMeta{decision: "allow"})
	writeJSON(w, 200, map[string]any{"rowsAffected": rows, "durationMs": duration})
}

// importColumnTypesSQL lists the data type of each column of the target table.
func importColumnTypesSQL(engineName, schema, table string) string {
	schemaSQL := "current_schema()"
	switch strings.ToLower(engineName) {
	case "mysql", "mariadb":
		schemaSQL = "DATABASE()"
	case "mssql", "sqlserver":
		schemaSQL = "SCHEMA_NAME()"
	}
	if schema != "" {
		schemaSQL = importLiteral(engineName, schema)
	}
	return "SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = " + schemaSQL + " AND table_name = " + importLiteral(engineName, table)
}

func importBinaryType(dataType string) bool {
	dataType = strings.ToUpper(dataType)
	if dataType == "BIT" {
		return true
	}
	for _, name := range []string{"BINARY", "BYTEA", "BLOB", "IMAGE", "GEOMETRY", "GEOGRAPHY", "HIERARCHYID", "POINT", "POLYGON", "LINESTRING", "MULTIPOINT", "COLLECTION"} {
		if strings.Contains(dataType, name) {
			return true
		}
	}
	return false
}

// hexText reports whether value is \x followed by whole bytes of hex.
func hexText(value string) bool {
	if !strings.HasPrefix(value, `\x`) || len(value)%2 != 0 {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil
}

func hexLiteral(engineName, digits string) string {
	switch strings.ToLower(engineName) {
	case "mysql", "mariadb":
		return "X'" + digits + "'"
	case "mssql", "sqlserver":
		return "0x" + digits
	default:
		return `'\x` + digits + `'`
	}
}

func importDelimiter(value string) (rune, error) {
	if value == "" {
		return ',', nil
	}
	if value == "\\t" {
		return '\t', nil
	}
	delimiter, size := utf8.DecodeRuneInString(value)
	if size != len(value) || !strings.ContainsRune(",;\t|", delimiter) {
		return 0, errors.New("delimiter must be comma, semicolon, tab or |")
	}
	return delimiter, nil
}

// quoteSQLIdentifier quotes a table or column name for the engine.
func quoteSQLIdentifier(engine, name string) string {
	switch strings.ToLower(engine) {
	case "mysql", "mariadb":
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	case "mssql", "sqlserver":
		return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
	default:
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
}

// importLiteral writes a CSV value as a string literal; each database converts
// it to the column type as it would a typed value.
func importLiteral(engine, value string) string {
	engine = strings.ToLower(engine)
	if engine == "mysql" || engine == "mariadb" {
		value = strings.ReplaceAll(value, `\`, `\\`)
	}
	value = strings.ReplaceAll(value, "'", "''")
	if engine == "mssql" || engine == "sqlserver" {
		return "N'" + value + "'"
	}
	return "'" + value + "'"
}
