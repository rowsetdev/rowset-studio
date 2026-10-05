package sqlguard_test

import (
	"testing"

	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

// A script is read as one statement carrying the riskiest kind in it and the
// union of its flags, so no statement can hide behind a harmless neighbour.
func TestScriptIsJudgedByItsRiskiestStatement(t *testing.T) {
	cases := []struct {
		sql      string
		kind     sqlguard.Kind
		where    bool
		drop     bool
		truncate bool
		session  bool
		tables   int
	}{
		// A DROP cannot hide behind a SELECT.
		{sql: "SELECT 1; DROP TABLE users", kind: sqlguard.DDL, drop: true},
		{sql: "SELECT 1; TRUNCATE TABLE users", kind: sqlguard.DDL, truncate: true, tables: 1},
		// Every statement of the deciding kind must have a WHERE.
		{sql: "DELETE FROM a WHERE id = 1; DELETE FROM b WHERE id = 2", kind: sqlguard.Delete, where: true, tables: 2},
		{sql: "DELETE FROM a; DELETE FROM b WHERE id = 1", kind: sqlguard.Delete, tables: 2},
		{sql: "UPDATE a SET x = 1; SELECT 1", kind: sqlguard.Update, tables: 1},
		// A write outranks a read, whichever order they come in.
		{sql: "DELETE FROM a WHERE id = 1; SELECT 1", kind: sqlguard.Delete, where: true, tables: 1},
		{sql: "SELECT 1; DELETE FROM a WHERE id = 1", kind: sqlguard.Delete, where: true, tables: 1},
		// Unrecognised SQL outranks everything: the script is judged as one
		// Rowset cannot read rather than by the parts it understood.
		// PREPARE builds a statement out of a string this parser cannot see
		// through, and leaves it on the connection.
		{sql: "SELECT 1; PREPARE s FROM @sql", kind: sqlguard.Other, session: true},
		// A maintenance script: session state, then a guarded delete.
		{sql: "SET NOCOUNT ON; DELETE FROM orders WHERE id < 10", kind: sqlguard.Delete, where: true, session: true, tables: 1},
		// Reads only.
		{sql: "SELECT 1; SELECT 2", kind: sqlguard.Select, tables: 0},
	}
	for _, item := range cases {
		info, err := sqlguard.ParseDialect(sqlguard.DialectMSSQL, item.sql)
		if err != nil {
			t.Fatalf("%q: %v", item.sql, err)
		}
		if !info.IsScript {
			t.Errorf("%q: IsScript = false", item.sql)
		}
		if info.Kind != item.kind {
			t.Errorf("%q: Kind = %s, want %s", item.sql, info.Kind, item.kind)
		}
		if info.HasWhere != item.where {
			t.Errorf("%q: HasWhere = %v, want %v", item.sql, info.HasWhere, item.where)
		}
		if info.IsDrop != item.drop {
			t.Errorf("%q: IsDrop = %v, want %v", item.sql, info.IsDrop, item.drop)
		}
		if info.IsTruncate != item.truncate {
			t.Errorf("%q: IsTruncate = %v, want %v", item.sql, info.IsTruncate, item.truncate)
		}
		if info.TouchesSession != item.session {
			t.Errorf("%q: TouchesSession = %v, want %v", item.sql, info.TouchesSession, item.session)
		}
		if len(info.Tables) != item.tables {
			t.Errorf("%q: %d tables %v, want %d", item.sql, len(info.Tables), info.Tables, item.tables)
		}
	}
}

// One statement is never a script, so nothing that reads a statement back out
// of its text changes behaviour for ordinary SQL.
func TestOneStatementIsNotAScript(t *testing.T) {
	for _, sql := range []string{
		"SELECT 1",
		"DELETE FROM orders WHERE id = 1",
		"SELECT 1;",
		"  DELETE FROM orders WHERE id = 1 ;  ",
		"BEGIN TRY DELETE FROM orders WHERE id = 1 END TRY",
		"WHILE 1 = 1 BEGIN DELETE FROM orders WHERE id < 5 END",
	} {
		info, err := sqlguard.ParseDialect(sqlguard.DialectMSSQL, sql)
		if err != nil {
			t.Fatalf("%q: %v", sql, err)
		}
		if info.IsScript {
			t.Errorf("%q read as a script", sql)
		}
	}
}

// A script is fingerprinted from its own text, so history and audit can group
// it; it is never fingerprinted as empty, which is what nil tokens would give.
func TestScriptsAreFingerprinted(t *testing.T) {
	first, _ := sqlguard.ParseDialect(sqlguard.DialectMSSQL, "SET NOCOUNT ON; DELETE FROM orders WHERE id < 10")
	again, _ := sqlguard.ParseDialect(sqlguard.DialectMSSQL, "SET NOCOUNT ON;\n DELETE FROM orders WHERE id < 10")
	other, _ := sqlguard.ParseDialect(sqlguard.DialectMSSQL, "SET NOCOUNT ON; DELETE FROM trades WHERE id < 10")
	normalized, hash := sqlguard.Normalize(first)
	_, reformatted := sqlguard.Normalize(again)
	_, different := sqlguard.Normalize(other)
	if normalized == "" || hash == "" {
		t.Fatalf("a script has no fingerprint: %q %q", normalized, hash)
	}
	if hash != reformatted {
		t.Errorf("the same script laid out differently has a different fingerprint")
	}
	if hash == different {
		t.Errorf("scripts against different tables share a fingerprint")
	}
}

// A script's token positions must point into the script itself. Anything that
// reads a statement back out of Raw by position - the row backup here,
// statement rewriting in a plugin - would otherwise slice the wrong text, and
// would do it silently.
func TestScriptTokenPositionsPointIntoItsOwnText(t *testing.T) {
	for _, sql := range []string{
		"SELECT 1; DELETE FROM orders WHERE id = 1",
		"SET NOCOUNT ON;\n\nSELECT name FROM sys.tables;\nSELECT 2",
		"SELECT 'a; b' AS q; SELECT 2",
	} {
		info, err := sqlguard.ParseDialect(sqlguard.DialectMSSQL, sql)
		if err != nil {
			t.Fatalf("%q: %v", sql, err)
		}
		if !info.IsScript {
			t.Fatalf("%q was not read as a script", sql)
		}
		if len(info.Tokens) == 0 {
			t.Fatalf("%q has no tokens", sql)
		}
		for _, token := range info.Tokens {
			if token.Start < 0 || token.End > len(info.Raw) || token.Start > token.End {
				t.Fatalf("%q: token %q spans [%d,%d) outside Raw of %d", sql, token.Text, token.Start, token.End, len(info.Raw))
			}
			if got := info.Raw[token.Start:token.End]; got != token.Text {
				t.Fatalf("%q: token %q does not match Raw[%d:%d] = %q", sql, token.Text, token.Start, token.End, got)
			}
		}
	}
}
