package sqlguard_test

import (
	"testing"

	sqlguard "github.com/rowsetdev/rowset-studio/rowset-core/sqlguard"
)

// A statement that leaves state on its connection must be recognised, so the
// server can give it a connection of its own instead of a pooled one, and
// transaction control must be recognised so it can be refused.
func TestSessionEffectsAreRecognised(t *testing.T) {
	cases := []struct {
		sql     string
		session bool
		txn     bool
		use     bool
	}{
		{sql: "SET NOCOUNT ON", session: true},
		{sql: "set @batch = 5000", session: true},
		{sql: "DECLARE @BatchSize int = 5000", session: true},
		{sql: "DECLARE reader CURSOR FOR SELECT id FROM orders", session: true},
		{sql: "FETCH NEXT FROM reader", session: true},
		{sql: "CLOSE reader", session: true},
		{sql: "DEALLOCATE reader", session: true},
		{sql: "PRINT 'done'", session: true},
		{sql: "RAISERROR('deleted %d', 0, 1, @n) WITH NOWAIT", session: true},
		{sql: "WAITFOR DELAY '00:00:00.010'", session: true},
		{sql: "CREATE TEMPORARY TABLE t (id int)", session: true},
		{sql: "SELECT id INTO #staging FROM orders WHERE id < 10", session: true},
		// SET opens the assignment list of every UPDATE; that leaves nothing
		// behind, so an ordinary write must keep its pooled connection.
		{sql: "UPDATE orders SET qty = 2 WHERE id = 1"},
		{sql: "UPDATE orders SET qty = 2, label = 'x' WHERE id = 1"},
		{sql: "INSERT INTO orders(qty) VALUES (1)"},
		{sql: "SELECT qty FROM orders WHERE id = 1"},
		{sql: "DELETE FROM orders WHERE id = 1"},
		{sql: "CREATE TABLE t (id int)"},
		// Transaction control is Rowset's own; the statement form is refused.
		{sql: "COMMIT", txn: true},
		{sql: "ROLLBACK", txn: true},
		{sql: "BEGIN TRANSACTION", txn: true},
		{sql: "BEGIN TRAN", txn: true},
		{sql: "START TRANSACTION", txn: true},
		{sql: "SAVEPOINT before_fix", txn: true},
		{sql: "RELEASE SAVEPOINT before_fix", txn: true},
		// A block that wraps real work is not transaction control.
		{sql: "BEGIN TRY DELETE FROM orders WHERE id = 1 END TRY"},
		{sql: "WHILE 1 = 1 BEGIN DELETE FROM orders WHERE id < 5 END"},
		{sql: "USE shop", use: true},
	}
	for _, item := range cases {
		info, err := sqlguard.ParseDialect(sqlguard.DialectMSSQL, item.sql)
		if err != nil {
			t.Fatalf("%q: %v", item.sql, err)
		}
		if info.TouchesSession != item.session {
			t.Errorf("%q: TouchesSession = %v, want %v", item.sql, info.TouchesSession, item.session)
		}
		if info.ControlsTransaction != item.txn {
			t.Errorf("%q: ControlsTransaction = %v, want %v", item.sql, info.ControlsTransaction, item.txn)
		}
		if info.SwitchesDatabase != item.use {
			t.Errorf("%q: SwitchesDatabase = %v, want %v", item.sql, info.SwitchesDatabase, item.use)
		}
	}
}

// A quoted identifier is a name, never a keyword, so a column called "set" or
// a table called "commit" must not be read as session or transaction control.
func TestQuotedNamesAreNotSessionControl(t *testing.T) {
	for _, sql := range []string{
		`SELECT "set" FROM orders WHERE id = 1`,
		`UPDATE orders SET "commit" = 1 WHERE id = 1`,
	} {
		info, err := sqlguard.ParseDialect(sqlguard.DialectPostgres, sql)
		if err != nil {
			t.Fatalf("%q: %v", sql, err)
		}
		if info.TouchesSession || info.ControlsTransaction || info.SwitchesDatabase {
			t.Errorf("%q: read as session control (session=%v txn=%v use=%v)", sql, info.TouchesSession, info.ControlsTransaction, info.SwitchesDatabase)
		}
	}
}
