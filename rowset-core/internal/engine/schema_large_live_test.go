package engine

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Opt-in catalog fixture: 500 tables, each with a key and four data columns.
// The audit harness records the measured load time in go test JSON output.
func TestLiveLargePostgresSchema(t *testing.T) {
	password := os.Getenv("ROWSET_MATRIX_POSTGRES_PASSWORD")
	if password == "" {
		t.Skip("ROWSET_MATRIX_POSTGRES_PASSWORD is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	m := NewManager()
	defer m.Close()
	c := Connection{ID: "large-schema", Engine: "postgres", Host: "127.0.0.1", Port: liveSQLPort(t, "postgres", 55432), Database: "rowset_e2e", Username: "postgres", Password: password}
	db, err := m.database(c)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.ExecContext(ctx, `DROP SCHEMA IF EXISTS rowset_large_fixture CASCADE`)
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA rowset_large_fixture`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS rowset_large_fixture CASCADE`)
	})
	_, err = db.ExecContext(ctx, `DO $$ BEGIN FOR n IN 1..500 LOOP EXECUTE format('CREATE TABLE rowset_large_fixture.table_%s (id integer PRIMARY KEY, a text, b integer, c timestamptz, d numeric)', n); END LOOP; END $$`)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	schema, err := m.Schema(ctx, c)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for table := range schema.Tables {
		if strings.HasPrefix(table, "rowset_large_fixture.") {
			count++
		}
	}
	t.Logf("large schema: %d tables, %d ms, %d warnings", count, elapsed.Milliseconds(), len(schema.Warnings))
	if count != 500 {
		t.Fatalf("loaded %d of 500 fixture tables", count)
	}
	if len(schema.Warnings) != 0 {
		t.Fatalf("metadata warnings: %v", schema.Warnings)
	}
}
