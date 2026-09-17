package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Studio draws these plans, so each engine must return the format its
// parser reads, and SQL Server must leave no pooled session in SHOWPLAN mode.
func TestLiveExplainReturnsReadablePlans(t *testing.T) {
	tests := []struct {
		engine, passwordEnv, user, table string
		port                             int
		setup                            []string
		estimated, actual                string
	}{
		{"postgres", "ROWSET_MATRIX_POSTGRES_PASSWORD", "postgres", "public.rowset_plan", 55432, []string{`DROP TABLE IF EXISTS public.rowset_plan`, `CREATE TABLE public.rowset_plan(id int primary key, name text)`}, `"Plan"`, `"Actual Rows"`},
		{"mysql", "ROWSET_MATRIX_MYSQL_PASSWORD", "root", "rowset_plan", 53306, []string{`DROP TABLE IF EXISTS rowset_plan`, `CREATE TABLE rowset_plan(id int primary key, name varchar(40))`}, `"query_block"`, "actual time"},
		{"mariadb", "ROWSET_MATRIX_MARIADB_PASSWORD", "root", "rowset_plan", 53307, []string{`DROP TABLE IF EXISTS rowset_plan`, `CREATE TABLE rowset_plan(id int primary key, name varchar(40))`}, `"query_block"`, `"r_loops"`},
		{"mssql", "ROWSET_MATRIX_MSSQL_PASSWORD", "sa", "dbo.rowset_plan", 51433, []string{`IF OBJECT_ID('dbo.rowset_plan','U') IS NOT NULL DROP TABLE dbo.rowset_plan`, `CREATE TABLE dbo.rowset_plan(id int primary key, name nvarchar(40))`}, "ShowPlanXML", "ActualRows"},
	}
	for _, test := range tests {
		t.Run(test.engine, func(t *testing.T) {
			password := os.Getenv(test.passwordEnv)
			if password == "" {
				t.Skip(test.passwordEnv + " is not configured")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			manager := NewManager()
			defer manager.Close()
			// One pooled connection, so a leftover SHOWPLAN session would be reused.
			connection := Connection{ID: "plan-" + test.engine, Engine: test.engine, Host: "127.0.0.1", Port: liveSQLPort(t, test.engine, test.port), Database: "rowset_e2e", Username: test.user, Password: password, PoolSize: 1}
			for _, statement := range test.setup {
				if _, err := manager.Execute(ctx, connection, statement, 0); err != nil {
					t.Fatal(err)
				}
			}
			query := fmt.Sprintf("SELECT id, name FROM %s WHERE id > 0;", test.table)
			plan, _, err := manager.Explain(ctx, connection, query, false)
			if err != nil || !strings.Contains(plan, test.estimated) {
				t.Fatalf("estimated plan: %v\n%.300s", err, plan)
			}
			plan, _, err = manager.Explain(ctx, connection, query, true)
			if test.actual == "" {
				if !errors.Is(err, ErrActualPlanUnsupported) {
					t.Fatalf("actual plan: %v", err)
				}
			} else if err != nil || !strings.Contains(plan, test.actual) {
				t.Fatalf("actual plan: %v\n%.300s", err, plan)
			}
			result, err := manager.Execute(ctx, connection, "SELECT 1 AS one", 0)
			if err != nil || len(result.Rows) != 1 || fmt.Sprint(result.Rows[0][0]) != "1" {
				t.Fatalf("pooled session changed after explain: %#v %v", result, err)
			}
		})
	}
}
