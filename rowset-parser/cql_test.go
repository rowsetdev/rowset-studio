package parser

import "testing"

func TestParseCQLStatements(t *testing.T) {
	tests := []struct {
		query      string
		kind       Kind
		hasWhere   bool
		isDrop     bool
		isTruncate bool
	}{
		{"SELECT * FROM users WHERE id = 1", Select, true, false, false},
		{"INSERT INTO users (id, name) VALUES (1, 'semi; colon')", Insert, false, false, false},
		{"UPDATE users SET name = 'x' WHERE id = 1", Update, true, false, false},
		{"DELETE FROM users WHERE id = 1", Delete, true, false, false},
		{"DROP TABLE users", DDL, false, true, false},
		{"TRUNCATE users", DDL, false, false, true},
	}
	for _, test := range tests {
		info, err := ParseCQL(test.query)
		if err != nil {
			t.Fatalf("ParseCQL(%q): %v", test.query, err)
		}
		if info.Kind != test.kind || info.HasWhere != test.hasWhere || info.IsDrop != test.isDrop || info.IsTruncate != test.isTruncate {
			t.Fatalf("ParseCQL(%q) = %+v", test.query, info)
		}
	}
}

func TestParseCQLBatch(t *testing.T) {
	info, err := ParseCQL("BEGIN UNLOGGED BATCH\nINSERT INTO users (id) VALUES (1);\nUPDATE users SET name='x' WHERE id=2;\nAPPLY BATCH;")
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != Update || !info.HasWhere || len(info.Tables) != 2 {
		t.Fatalf("unexpected batch info: %+v", info)
	}
}

func TestParseCQLRejectsUnsafeBatchShapes(t *testing.T) {
	tests := []string{
		"BEGIN BATCH INSERT INTO users (id) VALUES (1);",
		"BEGIN BATCH SELECT * FROM users; APPLY BATCH;",
		"SELECT * FROM users; DELETE FROM users WHERE id=1",
	}
	for _, query := range tests {
		info, err := ParseCQL(query)
		if err == nil && info.Kind != Multi {
			t.Fatalf("ParseCQL(%q) unexpectedly accepted as %s", query, info.Kind)
		}
	}
}
