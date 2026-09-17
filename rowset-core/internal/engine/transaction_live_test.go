package engine

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLivePinnedSessionsCommitAndRollbackAcrossEngines(t *testing.T) {
	tests := []struct {
		name, passwordEnv, user, database, table string
		port                                     int
		setup                                    []string
	}{
		{"postgres", "ROWSET_MATRIX_POSTGRES_PASSWORD", "postgres", "rowset_e2e", "public.rowset_tx_probe", 55432, []string{`DROP TABLE IF EXISTS public.rowset_tx_probe`, `CREATE TABLE public.rowset_tx_probe(id int primary key,note varchar(40))`}},
		{"mysql", "ROWSET_MATRIX_MYSQL_PASSWORD", "root", "rowset_e2e", "rowset_tx_probe", 53306, []string{`DROP TABLE IF EXISTS rowset_tx_probe`, `CREATE TABLE rowset_tx_probe(id int primary key,note varchar(40))`}},
		{"mariadb", "ROWSET_MATRIX_MARIADB_PASSWORD", "root", "rowset_e2e", "rowset_tx_probe", 53307, []string{`DROP TABLE IF EXISTS rowset_tx_probe`, `CREATE TABLE rowset_tx_probe(id int primary key,note varchar(40))`}},
		{"mssql", "ROWSET_MATRIX_MSSQL_PASSWORD", "sa", "rowset_e2e", "dbo.rowset_tx_probe", 51433, []string{`IF OBJECT_ID('dbo.rowset_tx_probe','U') IS NOT NULL DROP TABLE dbo.rowset_tx_probe`, `CREATE TABLE dbo.rowset_tx_probe(id int primary key,note varchar(40))`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			password := os.Getenv(test.passwordEnv)
			if password == "" {
				t.Skip(test.passwordEnv + " is not configured")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			manager := NewManager()
			defer manager.Close()
			connection := Connection{ID: "tx-" + test.name, Engine: test.name, Host: "127.0.0.1", Port: liveSQLPort(t, test.name, test.port), Database: test.database, Username: test.user, Password: password, PoolSize: 2}
			for _, statement := range test.setup {
				if _, err := manager.Execute(ctx, connection, statement, 0); err != nil {
					t.Fatal(err)
				}
			}
			session, err := manager.OpenSession(ctx, connection)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if err := session.Begin(TransactionOptions{Isolation: "serializable"}); err != nil {
				t.Fatal(err)
			}
			if _, err := session.Execute(ctx, fmt.Sprintf("INSERT INTO %s(id,note) VALUES(1,'rollback')", test.table), 0); err != nil {
				t.Fatal(err)
			}
			if count := sessionCount(t, ctx, session, test.table); count != 1 {
				t.Fatalf("transaction did not see its own row: %d", count)
			}
			if err := session.Rollback(); err != nil {
				t.Fatal(err)
			}
			if count := sessionCount(t, ctx, session, test.table); count != 0 {
				t.Fatalf("rollback left %d rows", count)
			}
			if err := session.Begin(TransactionOptions{Isolation: "read committed"}); err != nil {
				t.Fatal(err)
			}
			if _, err := session.Execute(ctx, fmt.Sprintf("INSERT INTO %s(id,note) VALUES(2,'commit')", test.table), 0); err != nil {
				t.Fatal(err)
			}
			if err := session.Commit(); err != nil {
				t.Fatal(err)
			}
			if count := sessionCount(t, ctx, session, test.table); count != 1 {
				t.Fatalf("commit exposed %d rows", count)
			}
		})
	}
}

func liveSQLPort(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw := os.Getenv("ROWSET_MATRIX_" + strings.ToUpper(name) + "_PORT")
	if raw == "" {
		return fallback
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		t.Fatalf("invalid live test port %q for %s", raw, name)
	}
	return port
}

func sessionCount(t *testing.T, ctx context.Context, session *Session, table string) int64 {
	t.Helper()
	stream, err := session.Query(ctx, "SELECT COUNT(*) FROM "+table)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	row, ok, err := stream.Next()
	if err != nil || !ok || len(row) != 1 {
		t.Fatalf("count result: row=%v ok=%v err=%v", row, ok, err)
	}
	switch value := row[0].(type) {
	case int64:
		return value
	case int32:
		return int64(value)
	case []byte:
		var parsed int64
		_, _ = fmt.Sscan(string(value), &parsed)
		return parsed
	default:
		var parsed int64
		_, _ = fmt.Sscan(fmt.Sprint(value), &parsed)
		return parsed
	}
}
