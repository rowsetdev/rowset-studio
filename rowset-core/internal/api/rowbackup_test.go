package api

import (
	"strings"
	"testing"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

func TestBackupTargetOf(t *testing.T) {
	cases := []struct {
		sql, kind, table, where string
	}{
		{"UPDATE orders SET total = 1 WHERE id = 5", "update", "orders", "id = 5"},
		{"update public.orders set total = total * 2 where status in ('a', 'b') returning id", "update", "orders", "status in ('a', 'b')"},
		{"DELETE FROM orders WHERE created_at < '2020-01-01' ORDER BY id LIMIT 10", "delete", "orders", "created_at < '2020-01-01'"},
		{"DELETE FROM orders WHERE id IN (SELECT id FROM orders WHERE total = 0);", "delete", "orders", "id IN (SELECT id FROM orders WHERE total = 0)"},
		{"UPDATE orders SET total = 0", "", "", ""},
		{"DELETE orders WHERE id = 1", "", "", ""},
		{"UPDATE o SET total = 0 FROM orders o JOIN customers c ON c.id = o.customer_id WHERE c.id = 1", "", "", ""},
		{"DELETE FROM orders USING customers WHERE orders.customer_id = customers.id", "", "", ""},
		{"WITH x AS (SELECT 1) UPDATE orders SET total = 0 WHERE id = 1", "", "", ""},
		{"SELECT * FROM orders WHERE id = 1", "", "", ""},
	}
	for _, test := range cases {
		info, err := sqlguard.Parse(test.sql)
		if err != nil {
			t.Fatalf("%s: %v", test.sql, err)
		}
		target, ok := backupTargetOf(info)
		if ok != (test.kind != "") {
			t.Fatalf("%s: ok = %v, target %+v", test.sql, ok, target)
		}
		if ok && (target.kind != test.kind || target.table != test.table || target.where != test.where) {
			t.Fatalf("%s: got %+v", test.sql, target)
		}
	}
}

func TestRestoreSQL(t *testing.T) {
	payload := rowBackupPayload{
		Statement: "UPDATE orders SET total = 0 WHERE id = 7",
		Columns:   []string{"id", "note", "total"},
		Key:       []string{"id"},
		Rows:      [][]backupValue{{{Kind: "num", Value: "7"}, {Kind: "text", Value: "it's"}, {Kind: "null"}}},
	}
	item := store.RowBackup{Table: "orders", Schema: "public", Kind: "update", Rows: 1}
	got := restoreSQL("postgres", item, payload)
	if !strings.Contains(got, `UPDATE "public"."orders" SET "note" = 'it''s', "total" = NULL WHERE "id" = 7;`) {
		t.Fatalf("update restore:\n%s", got)
	}
	item.Kind, item.Schema = "delete", ""
	got = restoreSQL("mssql", item, payload)
	if !strings.Contains(got, "INSERT INTO [orders] ([id], [note], [total]) VALUES\n  (7, N'it''s', NULL);") {
		t.Fatalf("delete restore:\n%s", got)
	}
	got = restoreSQL("mysql", item, rowBackupPayload{Columns: []string{"flag", "data"}, Rows: [][]backupValue{{{Kind: "bool", Value: "true"}, {Kind: "bytes", Value: "00ff"}}}})
	if !strings.Contains(got, "(1, X'00ff');") {
		t.Fatalf("mysql restore:\n%s", got)
	}
}
