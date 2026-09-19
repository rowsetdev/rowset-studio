package api

import (
	"strings"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

func TestCassandraBackupTargetOf(t *testing.T) {
	cases := []struct {
		cql, kind, table, where string
	}{
		{"UPDATE orders SET total = 1 WHERE id = 5", "update", "orders", "id = 5"},
		{"UPDATE ks.orders USING TTL 3600 SET total = 1 WHERE id = 5", "update", "orders", "id = 5"},
		{"UPDATE orders SET total = 1 WHERE id = 5 IF total = 0", "update", "orders", "id = 5"},
		{"DELETE FROM orders WHERE id = 5", "delete", "orders", "id = 5"},
		{"DELETE FROM ks.orders USING TIMESTAMP 100 WHERE id = 5", "delete", "orders", "id = 5"},
		{"DELETE FROM orders WHERE id = 5 IF EXISTS", "delete", "orders", "id = 5"},
		{"UPDATE orders SET total = 0", "", "", ""},
		{"SELECT * FROM orders WHERE id = 1", "", "", ""},
		{"BEGIN BATCH UPDATE orders SET total = 1 WHERE id = 1; UPDATE orders SET total = 2 WHERE id = 2; APPLY BATCH;", "", "", ""},
	}
	for _, test := range cases {
		info, err := sqlguard.ParseCQL(test.cql)
		if err != nil {
			t.Fatalf("%s: %v", test.cql, err)
		}
		target, ok := cassandraBackupTargetOf(info)
		if ok != (test.kind != "") {
			t.Fatalf("%s: ok = %v, target %+v", test.cql, ok, target)
		}
		if ok && (target.kind != test.kind || target.table != test.table || target.where != test.where) {
			t.Fatalf("%s: got %+v", test.cql, target)
		}
	}
}

func TestCassandraLiteral(t *testing.T) {
	cases := []struct {
		value backupValue
		want  string
	}{
		{backupValue{Kind: "null"}, "null"},
		{backupValue{Kind: "bool", Value: "true"}, "true"},
		{backupValue{Kind: "num", Value: "42"}, "42"},
		{backupValue{Kind: "uuid", Value: "550e8400-e29b-41d4-a716-446655440000"}, "550e8400-e29b-41d4-a716-446655440000"},
		{backupValue{Kind: "text", Value: "it's fine"}, "'it''s fine'"},
		{backupValue{Kind: "time", Value: "2024-01-01T00:00:00.000Z"}, "'2024-01-01T00:00:00.000Z'"},
		{backupValue{Kind: "bytes", Value: "AQIDBA=="}, "0x01020304"},
	}
	for _, test := range cases {
		if got := cassandraLiteral(test.value); got != test.want {
			t.Fatalf("%+v: got %q, want %q", test.value, got, test.want)
		}
	}
}

func TestCassandraRestoreStatements(t *testing.T) {
	payload := cassandraRowBackupPayload{
		Statement:  "UPDATE orders SET total = 0 WHERE id = 7",
		Keyspace:   "shop",
		Table:      "orders",
		PrimaryKey: []string{"id"},
		Columns:    []string{"id", "note", "total"},
		Rows: [][]backupValue{
			{{Kind: "num", Value: "7"}, {Kind: "text", Value: "hi"}, {Kind: "num", Value: "12"}},
		},
	}
	updateItem := store.RowBackup{Kind: "update", Table: "orders"}
	statements := cassandraRestoreStatements(updateItem, payload)
	if len(statements) != 1 {
		t.Fatalf("got %d statements, want 1: %v", len(statements), statements)
	}
	statement := statements[0]
	if !strings.HasPrefix(statement, `UPDATE "shop"."orders" SET `) || !strings.Contains(statement, `"note" = 'hi'`) || !strings.Contains(statement, `"total" = 12`) || !strings.HasSuffix(statement, `WHERE "id" = 7`) {
		t.Fatalf("unexpected UPDATE restore statement: %s", statement)
	}
	if strings.Contains(statement, `"id" = 7,`) || strings.Contains(statement, `SET "id"`) {
		t.Fatalf("primary key column must not be reassigned in SET: %s", statement)
	}

	deleteItem := store.RowBackup{Kind: "delete", Table: "orders"}
	statements = cassandraRestoreStatements(deleteItem, payload)
	if len(statements) != 1 {
		t.Fatalf("got %d statements, want 1: %v", len(statements), statements)
	}
	statement = statements[0]
	if !strings.HasPrefix(statement, `INSERT INTO "shop"."orders" ("id", "note", "total") VALUES (7, 'hi', 12)`) {
		t.Fatalf("unexpected DELETE restore statement: %s", statement)
	}
}

func TestCassandraRestoreBatchesChunkSize(t *testing.T) {
	statements := make([]string, 120)
	for i := range statements {
		statements[i] = "UPDATE t SET x = 1 WHERE id = 1"
	}
	batches := cassandraRestoreBatches(statements)
	if len(batches) != 3 {
		t.Fatalf("got %d batches for 120 statements at chunk 50, want 3", len(batches))
	}
	for _, batch := range batches {
		if !strings.HasPrefix(batch, "BEGIN BATCH\n") || !strings.HasSuffix(batch, "APPLY BATCH") {
			t.Fatalf("batch is not wrapped correctly: %s", batch)
		}
	}
}
