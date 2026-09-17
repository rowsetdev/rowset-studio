package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteFileQuerySchemaDDLAndRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database with space.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE parent(id INTEGER PRIMARY KEY); CREATE TABLE child(id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parent(id), label TEXT DEFAULT 'hello', calc TEXT GENERATED ALWAYS AS (label || '!') VIRTUAL); CREATE UNIQUE INDEX child_label ON child(label); CREATE VIEW child_names AS SELECT label FROM child; INSERT INTO parent VALUES (1); INSERT INTO child(id,parent_id,label) VALUES (9007199254740993,1,'before')`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager()
	defer m.Close()
	ctx := context.Background()
	connection := Connection{ID: "sqlite", Engine: "sqlite", Database: path}
	if err = m.Test(ctx, connection); err != nil {
		t.Fatal(err)
	}
	schema, err := m.Schema(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	columns := schema.Tables["main.child"]
	if len(columns) != 4 || !columns[0].PrimaryKey || columns[1].References != "main.parent.id" || columns[3].Generated == "" {
		t.Fatalf("columns: %#v", columns)
	}
	if !schema.Views["main.child_names"] || len(schema.Indexes["main.child"]) != 1 {
		t.Fatalf("schema: %#v", schema)
	}
	ddl, err := m.ObjectDDL(ctx, connection, "table", "main", "child")
	if err != nil || !strings.Contains(ddl, "GENERATED") {
		t.Fatalf("DDL: %s %v", ddl, err)
	}
	result, err := m.Execute(ctx, connection, "SELECT id,label FROM child", 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows[0][0] != int64(9007199254740993) {
		t.Fatalf("lost integer precision: %#v", result)
	}
	tx, err := m.Begin(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Execute(ctx, `UPDATE child SET label='after' WHERE id=9007199254740993`, 10); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	result, err = m.Execute(ctx, connection, "SELECT label FROM child", 10)
	if err != nil || result.Rows[0][0] != "before" {
		t.Fatalf("rollback: %#v %v", result, err)
	}
}

// DuckDB's transaction support was covered by the capability flag but never
// by a live test, unlike SQLite's equivalent above; this closes that gap.
func TestDuckDBFileQuerySchemaAndRollback(t *testing.T) {
	if !DuckDBAvailable {
		t.Skip("built without CGO; DuckDB is unavailable")
	}
	path := filepath.Join(t.TempDir(), "database.duckdb")
	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE child(id INTEGER PRIMARY KEY, label TEXT); INSERT INTO child VALUES (1,'before')`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager()
	defer m.Close()
	ctx := context.Background()
	connection := Connection{ID: "duckdb", Engine: "duckdb", Database: path}
	if err = m.Test(ctx, connection); err != nil {
		t.Fatal(err)
	}
	schema, err := m.Schema(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Tables["main.child"]) != 2 {
		t.Fatalf("columns: %#v", schema.Tables["main.child"])
	}
	tx, err := m.Begin(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Execute(ctx, `UPDATE child SET label='after' WHERE id=1`, 10); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	result, err := m.Execute(ctx, connection, "SELECT label FROM child", 10)
	if err != nil || result.Rows[0][0] != "before" {
		t.Fatalf("rollback: %#v %v", result, err)
	}
	tx, err = m.Begin(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Execute(ctx, `DELETE FROM child WHERE id=1`, 10); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	result, err = m.Execute(ctx, connection, "SELECT count(*) FROM child", 10)
	if err != nil || result.Rows[0][0] != int64(0) {
		t.Fatalf("commit: %#v %v", result, err)
	}
}

func TestDatabaseFilesDoNotCreateOnTypo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sqlite")
	m := NewManager()
	defer m.Close()
	if err := m.Test(context.Background(), Connection{Engine: "sqlite", Database: path}); err == nil {
		t.Fatal("missing file accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("missing file was created")
	}
	for _, path := range []string{"relative.sqlite", ":memory:", "/tmp/test?mode=ro"} {
		if ValidateDatabaseFile(path) == nil {
			t.Fatalf("accepted %s", path)
		}
	}
}

func TestMongoFilterUsesExtendedJSONAndRejectsJavaScript(t *testing.T) {
	filter, err := ParseMongoFilter(json.RawMessage(`{"_id":{"$oid":"507f1f77bcf86cd799439011"},"n":{"$numberLong":"9007199254740993"}}`))
	if err != nil || len(filter) != 2 {
		t.Fatalf("filter: %v %v", filter, err)
	}
	for _, raw := range []string{`[]`, `{"$where":"return true"}`, `{"$and":[{"$where":"return true"}]}`} {
		if _, err := ParseMongoFilter(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestLiveAdditionalServers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, kind := range []string{"clickhouse", "mongodb"} {
		t.Run(kind, func(t *testing.T) {
			host := os.Getenv("ROWSET_TEST_" + strings.ToUpper(kind) + "_HOST")
			if host == "" {
				t.Skip("additional test server not configured")
			}
			c := Connection{ID: kind, Engine: kind, Host: host, Port: 59000, Database: "default", Username: "default", TLS: TLSSettings{Mode: TLSDisable}}
			if kind == "clickhouse" {
				c.Password = os.Getenv("ROWSET_TEST_CLICKHOUSE_PASSWORD")
			}
			if kind == "mongodb" {
				c.Port = 57017
				c.Database = "rowset_test"
				c.Username = ""
			}
			c.Port = liveSQLPort(t, kind, c.Port)
			m := NewManager()
			defer m.Close()
			if err := m.Test(ctx, c); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Databases(ctx, c); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Schema(ctx, c); err != nil {
				t.Fatal(err)
			}
			if kind == "clickhouse" {
				r, err := m.Execute(ctx, c, "SELECT toUInt64(9007199254740993), toDecimal128('12345678901234567890.123456', 6)", 10)
				if err != nil {
					t.Fatal(err)
				}
				if len(r.Rows) != 1 {
					t.Fatalf("rows: %#v", r)
				}
			} else {
				if _, _, err := m.MongoFind(ctx, c, MongoFindInput{Collection: "items", Filter: json.RawMessage(`{}`), Limit: 10}); err != nil {
					t.Fatal(err)
				}
				if _, _, err := m.MongoFind(ctx, c, MongoFindInput{Collection: "items", Filter: json.RawMessage(`{}`), Project: json.RawMessage(`{"_id":1}`), Skip: 1, Limit: 10}); err != nil {
					t.Fatal(err)
				}
				if _, _, err := m.MongoFind(ctx, c, MongoFindInput{Collection: "items", Filter: json.RawMessage(`{}`), Skip: -1, Limit: 10}); err == nil {
					t.Fatal("expected a negative skip to be rejected")
				}
			}
		})
	}
}
