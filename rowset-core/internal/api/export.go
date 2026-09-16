package api

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dbaopsio/rowset-studio/rowset-core/internal/domain"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/engine"
	"github.com/dbaopsio/rowset-studio/rowset-core/internal/policy"
	sqlguard "github.com/dbaopsio/rowset-studio/rowset-parser"
)

type exportInput struct {
	Database string `json:"database"`
	Schema   string `json:"schema"`
	Table    string `json:"table"`
	// SQL exports the whole result of one SELECT instead of a table.
	SQL    string `json:"sql"`
	Format string `json:"format"`
}

// exportTable downloads a whole table as CSV or JSON. It runs SELECT * with
// the checks of the editor, so policies, row limits and result hooks apply.
// The file is written to a temporary file first, so a failure part way is
// reported as an error instead of a truncated download.
func (s *Server) exportTable(w http.ResponseWriter, r *http.Request) {
	defer s.holdAwake()()
	connection, ok := s.authorizedConnection(w, r)
	if !ok {
		return
	}
	var input exportInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Table, input.Schema, input.Database, input.SQL = strings.TrimSpace(input.Table), strings.TrimSpace(input.Schema), strings.TrimSpace(input.Database), strings.TrimSpace(input.SQL)
	if (input.Table == "" && input.SQL == "") || (input.Format != "csv" && input.Format != "json" && input.Format != "sql") {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "a table or a SELECT, and a csv, json or sql format, are required")
		return
	}
	if input.Format == "sql" && engine.AdditionalEngine(connection.Engine) {
		writeError(w, 400, "UNSUPPORTED", "SQL export is not available for this engine yet; use CSV or JSON")
		return
	}
	identity := identityFromContext(r.Context())
	role, err := s.store.UserRole(r.Context(), identity.UserID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "role missing")
		return
	}
	if connection.Engine == "cassandra" {
		s.exportCassandra(w, r, connection, role, input)
		return
	}
	sql, name := input.SQL, "query-result"
	// INSERT statements need a table to insert into: the exported table, or a
	// name made from the query's for a query export.
	target := qualifiedTable(connection.Engine, input.Schema, "exported_rows")
	if input.Table != "" {
		target = qualifiedTable(connection.Engine, input.Schema, input.Table)
	}
	if sql == "" {
		sql, name = "SELECT * FROM "+qualifiedTable(connection.Engine, input.Schema, input.Table), fileSlug(input.Table)
	}
	selected, err := s.openGovernedSelect(r.Context(), r, identity, role, connection, input.Database, sql, "export", 30*time.Minute)
	if err != nil {
		if errors.Is(err, errSelectBlocked) {
			writeError(w, http.StatusForbidden, "POLICY_DENIED", strings.TrimPrefix(err.Error(), errSelectBlocked.Error()+": "))
			return
		}
		writeError(w, http.StatusBadGateway, "EXEC_ERROR", err.Error())
		return
	}
	defer selected.Close()
	file, err := os.CreateTemp("", "rowset-export-*")
	if err != nil {
		selected.finish(0, err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the export file could not be created")
		return
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}()
	buffered := bufio.NewWriter(file)
	rows, truncated, err := writeRows(buffered, input.Format, selected.stream, selected.transforms, selected.limit, connection.Engine, target)
	if err == nil {
		err = buffered.Flush()
	}
	selected.finish(rows, err)
	if err != nil {
		writeError(w, http.StatusBadGateway, "EXEC_ERROR", err.Error())
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the export file could not be read")
		return
	}
	contentType := "text/csv; charset=utf-8"
	switch input.Format {
	case "json":
		contentType = "application/json"
	case "sql":
		contentType = "application/sql"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.%s"`, name, input.Format))
	w.Header().Set("X-Rowset-Rows", fmt.Sprint(rows))
	if truncated {
		w.Header().Set("X-Rowset-Truncated", "true")
	}
	_, _ = io.Copy(w, file)
}

func (s *Server) exportCassandra(w http.ResponseWriter, r *http.Request, connection domain.Connection, role domain.Role, input exportInput) {
	keyspace := input.Schema
	if keyspace == "" {
		keyspace = input.Database
	}
	if keyspace == "" {
		keyspace = connection.Database
	}
	quote := func(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
	query, name := input.SQL, "query-result"
	if query == "" {
		query, name = "SELECT * FROM "+quote(keyspace)+"."+quote(input.Table), fileSlug(input.Table)
	}
	info, err := sqlguard.ParseCQL(query)
	if err != nil || info.Kind != sqlguard.Select {
		writeError(w, 400, "BAD_REQUEST", "export requires one CQL SELECT")
		return
	}
	identity := identityFromContext(r.Context())
	disabled, enabled, limit, timeout, err := s.resolvePolicies(r, identity, connection)
	if err != nil {
		writeError(w, 500, "INTERNAL", "policies unavailable")
		return
	}
	decision := policy.Evaluate(policy.Input{Statement: info, Role: role.Name, ReadOnly: role.IsReadOnly || connection.ReadOnly, Environment: connection.Environment, Disabled: disabled, Enabled: enabled})
	if decision.Effect != policy.Allow {
		writePolicyError(w, 403, "POLICY_DENIED", decision, nil)
		return
	}
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	target, err := s.engineConnection(r, connection, keyspace)
	if err != nil {
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	ctx, cancel := withConnectionTimeout(r, connection, timeout, 30*time.Minute)
	defer cancel()
	result, err := s.engines.CassandraQuery(ctx, target, engine.CassandraQueryInput{Keyspace: keyspace, Query: query, Limit: limit})
	if err != nil {
		writeError(w, 502, "EXEC_ERROR", err.Error())
		return
	}
	var output bytes.Buffer
	switch input.Format {
	case "json":
		objects := make([]map[string]any, 0, len(result.Rows))
		for _, row := range result.Rows {
			object := map[string]any{}
			for i, column := range result.Columns {
				object[column] = row[i]
			}
			objects = append(objects, object)
		}
		_ = json.NewEncoder(&output).Encode(objects)
	case "csv":
		writer := csv.NewWriter(&output)
		_ = writer.Write(result.Columns)
		for _, row := range result.Rows {
			values := make([]string, len(row))
			for i, value := range row {
				values[i] = fmt.Sprint(value)
			}
			_ = writer.Write(values)
		}
		writer.Flush()
	}
	contentType := map[string]string{"csv": "text/csv; charset=utf-8", "json": "application/json", "sql": "application/sql"}[input.Format]
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.%s"`, name, input.Format))
	w.Header().Set("X-Rowset-Rows", fmt.Sprint(len(result.Rows)))
	if result.Truncated {
		w.Header().Set("X-Rowset-Truncated", "true")
	}
	_, _ = w.Write(output.Bytes())
}
