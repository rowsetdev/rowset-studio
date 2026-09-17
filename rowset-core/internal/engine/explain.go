package engine

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Plan formats returned by Explain.
const (
	PlanJSON = "json"
	PlanXML  = "xml"
	PlanText = "text"
)

// ErrActualPlanUnsupported reports an engine without actual-plan support.
var ErrActualPlanUnsupported = errors.New("actual execution plans are not available for this database yet")

// Explain returns the database's execution plan for one statement: JSON for
// PostgreSQL, MySQL and MariaDB, ShowPlanXML for SQL Server. Analyze runs the
// statement to report actual rows and timings (MySQL returns them as text).
func (m *Manager) Explain(ctx context.Context, connection Connection, statement string, analyze bool) (string, string, error) {
	db, err := m.database(connection)
	if err != nil {
		return "", "", err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", "", err
	}
	defer conn.Close()
	statement = strings.TrimRight(strings.TrimSpace(statement), "; \t\r\n")
	var query, format string
	switch strings.ToLower(connection.Engine) {
	case "postgres":
		query, format = "EXPLAIN (FORMAT JSON) "+statement, PlanJSON
		if analyze {
			query = "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) " + statement
		}
	case "mysql":
		query, format = "EXPLAIN FORMAT=JSON "+statement, PlanJSON
		if analyze {
			query, format = "EXPLAIN ANALYZE "+statement, PlanText
		}
	case "mariadb":
		query, format = "EXPLAIN FORMAT=JSON "+statement, PlanJSON
		if analyze {
			query = "ANALYZE FORMAT=JSON " + statement
		}
	case "mssql", "sqlserver":
		return sqlServerPlan(ctx, conn, statement, analyze)
	case "cockroachdb":
		query, format = "EXPLAIN "+statement, PlanText
		if analyze {
			query = "EXPLAIN ANALYZE " + statement
		}
	case "clickhouse":
		if analyze {
			return "", "", fmt.Errorf("actual execution plans are not supported for %s", connection.Engine)
		}
		query, format = "EXPLAIN "+statement, PlanText
	default:
		return "", "", fmt.Errorf("execution plans are not supported for %s", connection.Engine)
	}
	plan, err := readPlan(ctx, conn, query)
	return plan, format, err
}

// sqlServerPlan reads the estimated plan. SHOWPLAN_XML is session state, so a
// connection that cannot switch it off again is discarded instead of being
// returned to the pool, where it would answer queries with plans.
func sqlServerPlan(ctx context.Context, conn *sql.Conn, statement string, analyze bool) (string, string, error) {
	setting := "SHOWPLAN_XML"
	if analyze {
		setting = "STATISTICS XML"
	}
	if _, err := conn.ExecContext(ctx, "SET "+setting+" ON"); err != nil {
		return "", "", err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(cleanupCtx, "SET "+setting+" OFF"); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if analyze {
		plan, err := readActualSQLServerPlan(ctx, conn, statement)
		return plan, PlanXML, err
	}
	plan, err := readPlan(ctx, conn, statement)
	return plan, PlanXML, err
}

// STATISTICS XML appends a plan result set after the query's own rows. Drain
// the data without retaining it; only the ShowPlan XML belongs in the answer.
func readActualSQLServerPlan(ctx context.Context, conn *sql.Conn, statement string) (string, error) {
	rows, err := conn.QueryContext(ctx, statement)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var plan string
	for {
		columns, err := rows.Columns()
		if err != nil {
			return "", err
		}
		for rows.Next() {
			values := make([]any, len(columns))
			targets := make([]any, len(columns))
			for i := range values {
				targets[i] = &values[i]
			}
			if err := rows.Scan(targets...); err != nil {
				return "", err
			}
			if len(values) == 1 {
				var value string
				switch raw := values[0].(type) {
				case string:
					value = raw
				case []byte:
					value = string(raw)
				}
				if strings.Contains(value, "<ShowPlanXML") {
					plan = value
				}
			}
		}
		if err := rows.Err(); err != nil {
			return "", err
		}
		if !rows.NextResultSet() {
			break
		}
	}
	if plan == "" {
		return "", errors.New("SQL Server returned no actual ShowPlan XML")
	}
	return plan, nil
}

// readPlan joins the first column of every row of every result set.
func readPlan(ctx context.Context, conn *sql.Conn, query string) (string, error) {
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var parts []string
	for {
		columns, err := rows.Columns()
		if err != nil {
			return "", err
		}
		for rows.Next() {
			values := make([]any, len(columns))
			targets := make([]any, len(columns))
			for index := range values {
				targets[index] = &values[index]
			}
			if err := rows.Scan(targets...); err != nil {
				return "", err
			}
			if len(values) == 0 {
				continue
			}
			switch value := values[0].(type) {
			case []byte:
				parts = append(parts, string(value))
			case nil:
			default:
				parts = append(parts, fmt.Sprint(value))
			}
		}
		if !rows.NextResultSet() {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(parts) == 0 {
		return "", errors.New("the database returned no execution plan")
	}
	return strings.Join(parts, "\n"), nil
}
