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

// A float must survive an SQL export → reimport round trip exactly.
func TestSqlValueLiteralKeepsFloatPrecision(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  string
	}{{1234.123456789, "1234.123456789"}, {float32(0.1), "0.1"}, {1e-9, "0.000000001"}, {-2.5, "-2.5"}, {3.0, "3"}} {
		if got := sqlValueLiteral("postgres", tc.value); got != tc.want {
			t.Errorf("sqlValueLiteral(%v) = %q, want %q", tc.value, got, tc.want)
		}
	}
}
