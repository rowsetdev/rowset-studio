package api

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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

// scheduleTick is how often due scheduled queries are looked up.
const scheduleTick = 15 * time.Second

// scheduler starts due scheduled queries while Rowset runs. A run missed while
// Rowset was closed runs once at start when the query asks for it; otherwise
// the query waits for its next time.
func (s *Server) scheduler(ctx context.Context) {
	ticker := time.NewTicker(scheduleTick)
	defer ticker.Stop()
	s.startDueSchedules(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.startDueSchedules(ctx, now)
		}
	}
}

func (s *Server) startDueSchedules(ctx context.Context, now time.Time) {
	now = now.UTC()
	items, err := s.store.DueScheduledQueries(ctx, now.Format(time.RFC3339))
	if err != nil {
		return
	}
	for _, item := range items {
		var spec scheduleSpec
		if json.Unmarshal([]byte(item.Schedule), &spec) != nil {
			continue
		}
		// Advance first, so a slow or failing run is never started twice.
		next, err := nextRunString(spec, true, now)
		if err != nil {
			continue
		}
		if s.store.SetScheduledNextRun(ctx, item.ID, next) != nil {
			continue
		}
		due, err := time.Parse(time.RFC3339, *item.NextRunAt)
		if err == nil && now.Sub(due) > 2*scheduleTick && !item.CatchUp {
			continue
		}
		s.startScheduled(item, "schedule")
	}
}

// startScheduled runs the query in the background unless it is running.
func (s *Server) startScheduled(item store.ScheduledQuery, trigger string) bool {
	s.scheduleMu.Lock()
	if s.scheduleRunning[item.ID] || s.scheduleContext == nil || s.scheduleContext.Err() != nil {
		s.scheduleMu.Unlock()
		return false
	}
	s.scheduleRunning[item.ID] = true
	s.scheduleRuns.Add(1)
	s.scheduleMu.Unlock()
	go func() {
		defer s.scheduleRuns.Done()
		defer func() {
			s.scheduleMu.Lock()
			delete(s.scheduleRunning, item.ID)
			s.scheduleMu.Unlock()
		}()
		s.runScheduled(s.scheduleContext, item, trigger)
	}()
	return true
}

func (s *Server) runScheduled(ctx context.Context, item store.ScheduledQuery, trigger string) {
	run := store.ScheduledRun{ID: id.New(), QueryID: item.ID, Trigger: trigger, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Status: "running"}
	if err := s.store.CreateScheduledRun(ctx, run); err != nil {
		s.logger.Error("scheduled query run not recorded", "query", item.ID, "error", err)
		return
	}
	started := time.Now()
	rows, path, err := s.executeScheduled(ctx, item)
	finished := time.Now().UTC().Format(time.RFC3339Nano)
	go s.notifyScheduledRun(item.UserID, item.Name, rows, time.Since(started), path, err)
	run.FinishedAt, run.Rows = &finished, rows
	if err != nil {
		message := err.Error()
		run.Status, run.Error = "error", &message
	} else {
		run.Status, run.OutputPath = "success", &path
	}
	if err := s.store.FinishScheduledRun(context.Background(), run); err != nil {
		s.logger.Error("scheduled query run not finished", "query", item.ID, "error", err)
	}
}

// executeScheduled runs the query for its owner through the same checks as
// the editor (access, policies, statement and result hooks) and writes the
// result to a new file in the output folder.
func (s *Server) executeScheduled(ctx context.Context, item store.ScheduledQuery) (int64, string, error) {
	defer s.holdAwake()()
	sql, err := s.openScheduledSQL(item)
	if err != nil {
		return 0, "", err
	}
	user, err := s.store.User(ctx, item.UserID)
	if err != nil || user.Status != "active" {
		return 0, "", errors.New("the owner of this query is no longer active")
	}
	role, err := s.store.UserRole(ctx, user.ID)
	if err != nil {
		return 0, "", errors.New("the owner has no role")
	}
	connection, err := s.store.Connection(ctx, item.ConnectionID)
	if err != nil {
		return 0, "", errors.New("the connection no longer exists")
	}
	if !engine.SupportsScheduledSQL(connection.Engine) {
		return 0, "", errors.New("scheduled queries are not available for this engine")
	}
	identity := domain.Identity{UserID: user.ID, OrgID: user.OrgID, Email: user.Email, Role: role.Name}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "/scheduled", nil)
	if err != nil {
		return 0, "", err
	}
	if !s.canUseConnection(r, identity, role.ID, connection) {
		return 0, "", errors.New("the owner no longer has access to the connection")
	}
	selected, err := s.openGovernedSelect(ctx, r, identity, role, connection, item.Database, sql, "scheduler", 30*time.Minute)
	if err != nil {
		return 0, "", err
	}
	defer selected.Close()
	rows, path, err := writeScheduledResult(item, selected.stream, selected.transforms, selected.limit)
	selected.finish(rows, err)
	return rows, path, err
}

// errSelectBlocked marks a SELECT that policies do not allow.
var errSelectBlocked = errors.New("blocked by policy")

// governedSelect is one SELECT opened with the checks the editor applies:
// policies (including their row limit), statement hooks and result hooks.
type governedSelect struct {
	stream     *engine.RowStream
	transforms ResultTransforms
	// limit is the policy's row cap, or 0 when a policy sets none.
	limit  int
	cancel context.CancelFunc
	finish func(rows int64, err error)
}

func (g *governedSelect) Close() {
	g.stream.Close()
	g.cancel()
}

// openGovernedSelect runs sql, which must be one SELECT, for a caller that has
// already checked access to the connection. finish records the activity.
func (s *Server) openGovernedSelect(ctx context.Context, r *http.Request, identity domain.Identity, role domain.Role, connection domain.Connection, database, sql, source string, timeout time.Duration) (*governedSelect, error) {
	info, err := sqlguard.ParseDialect(sqlguard.DialectForEngine(connection.Engine), sql)
	if err != nil {
		return nil, err
	}
	if info.Kind != sqlguard.Select {
		return nil, errors.New("only one SELECT statement can run here")
	}
	normalized, hash := sqlguard.Normalize(info)
	disabled, enabled, rowLimit, policyTimeout, err := s.resolvePolicies(r, identity, connection)
	if err != nil {
		return nil, errors.New("governance rules unavailable")
	}
	decision := policy.Evaluate(policy.Input{Statement: info, Role: role.Name, ReadOnly: role.IsReadOnly || connection.ReadOnly, Environment: connection.Environment, Disabled: disabled, Enabled: enabled})
	if !s.config.Shared || !identity.IsAdmin() {
		if decision, rowLimit, err = s.applyCustomPolicies(r, identity, connection, info, false, decision, rowLimit); err != nil {
			return nil, errors.New("governance rules unavailable")
		}
	}
	if decision.Effect != policy.Allow {
		s.recordClientActivity(ctx, identity, connection, sql, normalized, hash, source, "", "", 0, 0, false, "blocked", decision, "deny", "", decision.Reason)
		return nil, fmt.Errorf("%w: %s", errSelectBlocked, decision.Reason)
	}
	prepared, _, err := s.prepareStatement(ctx, StatementRequest{Identity: identity, RoleID: role.ID, Connection: connection, Statement: info})
	if err != nil {
		return nil, err
	}
	effectiveSQL := prepared.Raw
	target, _, err := s.routedEngineConnection(ctx, identity, connection, database, nil, &prepared)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := withConnectionTimeout(r, connection, policyTimeout, timeout)
	stream, err := s.engines.Query(runCtx, target, effectiveSQL)
	if err != nil {
		cancel()
		s.recordClientActivity(ctx, identity, connection, sql, normalized, hash, source, "", "", 0, 0, false, "error", decision, "allow", "", err.Error())
		return nil, err
	}
	transforms, _, err := s.prepareResult(ctx, ResultRequest{Identity: identity, Connection: connection, Statement: prepared, Columns: stream.Columns(), Origins: stream.ColumnOrigins()})
	if err != nil {
		stream.Close()
		cancel()
		return nil, err
	}
	finish := func(rows int64, err error) {
		status, message := "success", ""
		if err != nil {
			status, message = "error", err.Error()
		}
		s.recordClientActivity(ctx, identity, connection, sql, normalized, hash, source, "", "", rows, stream.DurationMS(), false, status, decision, "allow", "", message)
	}
	return &governedSelect{stream: stream, transforms: transforms, limit: rowLimit, cancel: cancel, finish: finish}, nil
}

// writeScheduledResult streams rows into "<name>_<local time>.<format>". The
// file appears under its final name only once it is complete.
func writeScheduledResult(item store.ScheduledQuery, stream queryRowStream, transforms ResultTransforms, limit int) (int64, string, error) {
	if err := os.MkdirAll(item.OutputDir, 0o755); err != nil {
		return 0, "", fmt.Errorf("output folder: %w", err)
	}
	var spec scheduleSpec
	_ = json.Unmarshal([]byte(item.Schedule), &spec)
	location, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		location = time.UTC
	}
	name := fmt.Sprintf("%s_%s.%s", fileSlug(item.Name), time.Now().In(location).Format("2006-01-02_150405"), item.OutputFormat)
	path := filepath.Join(item.OutputDir, name)
	partial := path + ".partial"
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, "", fmt.Errorf("output file: %w", err)
	}
	buffered := bufio.NewWriter(file)
	rows, _, writeErr := writeRows(buffered, item.OutputFormat, stream, transforms, limit, "", "exported_rows")
	if writeErr == nil {
		writeErr = buffered.Flush()
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(partial)
		return rows, "", writeErr
	}
	if err := os.Rename(partial, path); err != nil {
		_ = os.Remove(partial)
		return rows, "", err
	}
	return rows, path, nil
}

// writeRows writes every row, or limit rows when a policy caps the result.
// It reports whether rows were left behind.
func writeRows(out *bufio.Writer, format string, stream queryRowStream, transforms ResultTransforms, limit int, engine, table string) (int64, bool, error) {
	columns := stream.Columns()
	var rows int64
	truncated := false
	if format == "sql" {
		return writeInsertRows(out, stream, transforms, limit, engine, table, columns)
	}
	if format == "json" {
		if _, err := out.WriteString("[\n"); err != nil {
			return 0, truncated, err
		}
		for {
			if limit > 0 && rows >= int64(limit) {
				truncated = true
				break
			}
			row, ok, err := stream.Next()
			if err != nil {
				return rows, truncated, err
			}
			if !ok {
				break
			}
			transforms.apply(row)
			object := make(map[string]any, len(columns))
			for index, column := range columns {
				if index < len(row) {
					object[column] = fileValue(row[index])
				}
			}
			encoded, err := json.Marshal(object)
			if err != nil {
				return rows, truncated, err
			}
			if rows > 0 {
				if _, err := out.WriteString(",\n"); err != nil {
					return rows, truncated, err
				}
			}
			if _, err := out.Write(encoded); err != nil {
				return rows, truncated, err
			}
			rows++
		}
		_, err := out.WriteString("\n]\n")
		return rows, truncated, err
	}
	// A UTF-8 byte-order mark so Excel opens the file as UTF-8; without it
	// non-ASCII text (Turkish, say) is shown in the wrong encoding.
	if _, err := out.WriteString("\ufeff"); err != nil {
		return 0, truncated, err
	}
	writer := csv.NewWriter(out)
	if err := writer.Write(columns); err != nil {
		return 0, truncated, err
	}
	record := make([]string, len(columns))
	for {
		if limit > 0 && rows >= int64(limit) {
			truncated = true
			break
		}
		row, ok, err := stream.Next()
		if err != nil {
			return rows, truncated, err
		}
		if !ok {
			break
		}
		transforms.apply(row)
		for index := range record {
			record[index] = ""
			if index < len(row) && row[index] != nil {
				record[index] = fmt.Sprint(fileValue(row[index]))
			}
		}
		if err := writer.Write(record); err != nil {
			return rows, truncated, err
		}
		rows++
	}
	writer.Flush()
	return rows, truncated, writer.Error()
}

// writeInsertRows writes the result as INSERT statements for the table,
// batching rows so the file is a handful of multi-row inserts rather than one
// statement per row.
func writeInsertRows(out *bufio.Writer, stream queryRowStream, transforms ResultTransforms, limit int, engine, table string, columns []string) (int64, bool, error) {
	const batch = 200
	quoted := make([]string, len(columns))
	for index, column := range columns {
		quoted[index] = quoteSQLIdentifier(engine, column)
	}
	prefix := "INSERT INTO " + table + " (" + strings.Join(quoted, ", ") + ") VALUES\n"
	var rows int64
	inBatch := 0
	truncated := false
	for {
		if limit > 0 && rows >= int64(limit) {
			truncated = true
			break
		}
		row, ok, err := stream.Next()
		if err != nil {
			return rows, truncated, err
		}
		if !ok {
			break
		}
		transforms.apply(row)
		if inBatch == 0 {
			if _, err := out.WriteString(prefix); err != nil {
				return rows, truncated, err
			}
		} else if _, err := out.WriteString(",\n"); err != nil {
			return rows, truncated, err
		}
		values := make([]string, len(columns))
		for index := range values {
			var value any
			if index < len(row) {
				value = row[index]
			}
			values[index] = sqlValueLiteral(engine, value)
		}
		if _, err := out.WriteString("  (" + strings.Join(values, ", ") + ")"); err != nil {
			return rows, truncated, err
		}
		rows++
		inBatch++
		if inBatch == batch {
			if _, err := out.WriteString(";\n"); err != nil {
				return rows, truncated, err
			}
			inBatch = 0
		}
	}
	if inBatch > 0 {
		if _, err := out.WriteString(";\n"); err != nil {
			return rows, truncated, err
		}
	}
	return rows, truncated, nil
}

// sqlValueLiteral renders one value as a SQL literal: NULL for nothing, an
// unquoted number for numeric types, 1/0 for a boolean, and an escaped string
// otherwise.
func sqlValueLiteral(engine string, value any) string {
	switch v := fileValue(value).(type) {
	case nil:
		return "NULL"
	case bool:
		if v {
			return "1"
		}
		return "0"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", v)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		if hexText(v) {
			return hexLiteral(engine, v[2:])
		}
		return importLiteral(engine, v)
	default:
		return importLiteral(engine, fmt.Sprint(v))
	}
}

func fileValue(value any) any {
	switch v := value.(type) {
	case []byte:
		return string(v)
	case time.Time:
		return v.Format(time.RFC3339Nano)
	default:
		return v
	}
}

// fileSlug turns a query name into a safe file-name prefix.
func fileSlug(name string) string {
	var out strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
			dash = false
		case !dash && out.Len() > 0:
			out.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(out.String(), "-")
	if slug == "" {
		return "query"
	}
	if len(slug) > 60 {
		slug = strings.Trim(slug[:60], "-")
	}
	return slug
}
