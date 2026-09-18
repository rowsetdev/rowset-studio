package api

import "testing"

// A binary column's driver value has already been normalized to a
// "\xdeadbeef"-shaped Go string (engine.normalizeValues) by the time
// sqlValueLiteral sees it. Each SQL engine spells a binary literal
// differently; sqlValueLiteral must match what restoreLiteral (row-backup
// restore) and hexLiteral (CSV import) already do, or the generated INSERT
// re-reads as a text string instead of the original bytes.
func TestSqlValueLiteralBinaryPerEngine(t *testing.T) {
	const normalized = `\xdeadbeef`
	cases := []struct {
		engine string
		want   string
	}{
		{"mysql", "X'deadbeef'"},
		{"mariadb", "X'deadbeef'"},
		{"mssql", "0xdeadbeef"},
		{"sqlserver", "0xdeadbeef"},
		{"postgres", `'\xdeadbeef'`},
	}
	for _, tc := range cases {
		if got := sqlValueLiteral(tc.engine, normalized); got != tc.want {
			t.Errorf("engine %s: sqlValueLiteral(%q) = %q, want %q", tc.engine, normalized, got, tc.want)
		}
	}
}

// A plain string that merely looks like other data must not be mistaken for
// a binary marker and must still be quoted as an ordinary string literal.
func TestSqlValueLiteralOrdinaryStringUnaffected(t *testing.T) {
	if got, want := sqlValueLiteral("mysql", "hello world"), "'hello world'"; got != want {
		t.Errorf("sqlValueLiteral(ordinary string) = %q, want %q", got, want)
	}
}
