package sqlguard

import "testing"

func TestClassificationIgnoresCommentsAndStrings(t *testing.T) {
	tests := []struct {
		sql   string
		kind  Kind
		where bool
	}{
		{"SELECT * FROM public.users WHERE id = 1", Select, true},
		{"SELECT '-- delete from x' FROM users", Select, false},
		{"SELECT $$; DELETE FROM hidden$$ FROM users", Select, false},
		{"SELECT $tag$ UPDATE hidden $tag$ FROM users", Select, false},
		{"SELECT 1 # DELETE FROM hidden\nFROM users", Select, false},
		{"/* outer /* DELETE hidden */ still comment */ SELECT 1", Select, false},
		{"SELECT \"odd\"\"delete\" FROM users", Select, false},
		{"/* UPDATE x */ DELETE FROM users WHERE id=1", Delete, true},
		{"WITH changed AS (UPDATE users SET ok=true RETURNING *) SELECT * FROM changed", Update, false},
		{"WITH changed AS (UPDATE users SET ok=true WHERE id=1 RETURNING *) SELECT * FROM changed WHERE id=1", Update, true},
		{"WITH users AS (SELECT * FROM users WHERE id=1) SELECT * FROM users FOR UPDATE", Select, false},
		{"SELECT 1; DELETE FROM users", Multi, false},
		{"DROP TABLE users", DDL, false},
	}
	for _, tt := range tests {
		got, _ := Parse(tt.sql)
		if got.Kind != tt.kind || got.HasWhere != tt.where {
			t.Fatalf("%q: got kind=%s where=%v", tt.sql, got.Kind, got.HasWhere)
		}
	}
}

func TestVendorMutationCommandsAreWrites(t *testing.T) {
	queries := []string{
		"DO $$ BEGIN UPDATE users SET active=false; END $$",
		"LOAD DATA INFILE 'users.csv' INTO TABLE users",
		"BULK INSERT users FROM 'users.csv'",
		"OPTIMIZE TABLE events FINAL",
		"SYSTEM RELOAD CONFIG",
		"PUT file:///tmp/users.csv @stage",
		"REMOVE @stage/users.csv",
		"REFRESH MATERIALIZED VIEW totals",
		"DBCC SHRINKDATABASE(app)",
	}
	for _, query := range queries {
		info, err := Parse(query)
		if err != nil {
			t.Fatalf("Parse(%q): %v", query, err)
		}
		if info.Kind != DDL || !IsWrite(info.Kind) {
			t.Fatalf("%q classified as %s", query, info.Kind)
		}
	}
}

func TestSessionControlClassification(t *testing.T) {
	for _, query := range []string{"SET QUOTED_IDENTIFIER OFF", "BEGIN", "COMMIT", "ROLLBACK", "SAVEPOINT before_edit", "USE archive"} {
		info, err := Parse(query)
		if err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if info.Kind != Session || IsWrite(info.Kind) {
			t.Fatalf("%s classified as %s (write=%v), want non-write session control", query, info.Kind, IsWrite(info.Kind))
		}
	}
}

func TestCTERiskAndOuterCommandAreDistinct(t *testing.T) {
	rows, err := Parse("WITH changed AS (UPDATE users SET ok=true WHERE id=1 RETURNING *) SELECT * FROM changed WHERE id=1")
	if err != nil || rows.Kind != Update || rows.Command != Select || !rows.HasWhere {
		t.Fatalf("data-changing CTE classification: %#v %v", rows, err)
	}
	command, err := Parse("WITH target AS (SELECT id FROM users WHERE id=1) UPDATE users SET ok=true WHERE id IN (SELECT id FROM target)")
	if err != nil || command.Kind != Update || command.Command != Update || !command.HasWhere {
		t.Fatalf("CTE update classification: %#v %v", command, err)
	}
	unsafe, err := Parse("WITH changed AS (UPDATE users SET ok=true RETURNING *) SELECT * FROM changed WHERE id=1")
	if err != nil || unsafe.HasWhere {
		t.Fatalf("outer WHERE disguised an unbounded CTE write: %#v %v", unsafe, err)
	}
}

func TestExtractTablesAndSecondarySafety(t *testing.T) {
	info, err := Parse("select u.id from public.users as u join groups t on t.id=u.group_id where u.id=1")
	if err != nil || len(info.Tables) != 2 || info.Tables[0].Schema != "public" || info.Tables[0].Alias != "u" {
		t.Fatalf("unexpected: %#v %v", info, err)
	}
	unsafe, _ := Parse("select * from users for update")
	if SecondarySafe(unsafe) {
		t.Fatal("SELECT FOR UPDATE must not use a secondary")
	}
}

func TestMalformedSQLFailsClosed(t *testing.T) {
	for _, sql := range []string{"select 'oops", "select (1", "/* no end", "select $tag$ no end"} {
		info, err := Parse(sql)
		if err == nil || info.Kind != Unknown {
			t.Fatalf("%q did not fail closed", sql)
		}
	}
}

func TestTautologicalWhereDoesNotBypassWriteGuard(t *testing.T) {
	for _, sql := range []string{"update users set ok=(select true where id=1)", "update users set ok=true where 1=1", "delete from users where true", "delete from users where id=7 or 1=1", "delete from users where (1=1 and 2=2)", "delete from users where false or (3 > 2)", "delete from users where 1 <> 2", "delete from users where 'a' != 'b'", "delete from users where 2 >= 1"} {
		info, err := Parse(sql)
		if err != nil || info.HasWhere {
			t.Fatalf("%q bypassed: %#v %v", sql, info, err)
		}
	}
	for _, sql := range []string{"delete from users where 1=1 and id=2", "delete from users where false", "delete from users where false or id=2", "update users set a=1 where id <= 2", "delete from users where status != 'x'", "delete from users where id <> 7", "delete from users where id >= 3", "delete from users where 1 <= 0"} {
		info, err := Parse(sql)
		if err != nil || !info.HasWhere {
			t.Fatalf("%q was incorrectly treated as tautological: %#v %v", sql, info, err)
		}
	}
}

func TestQuotedIdentifiersAreNotKeywords(t *testing.T) {
	for _, sql := range []string{`select * from secrets as "where"`, `delete from t as "where"`, "delete from t as `where`", `update t as [where] set a = 1`, `delete from t where "x" = 1 or 1=1`, `delete from t where "limit" is null or 1=1`} {
		info, err := Parse(sql)
		if err != nil || info.HasWhere {
			t.Fatalf("%q bypassed the WHERE guard: %#v %v", sql, info, err)
		}
	}
	for _, sql := range []string{`delete from t where "true"`, `delete from t where not id = 5`, `delete from t where not (a = 1 or b = 2)`, `update t set "delete" = 1 where id = 2`} {
		info, err := Parse(sql)
		if err != nil || !info.HasWhere {
			t.Fatalf("%q was incorrectly treated as unfiltered: %#v %v", sql, info, err)
		}
	}
}

func TestDialectCorpusKeepsRiskClassificationStable(t *testing.T) {
	tests := []struct {
		engine string
		sql    string
		kind   Kind
		cmd    Kind
	}{
		{"postgres", `SELECT DISTINCT ON (tenant_id) tenant_id, email FROM public.customers WHERE active IS TRUE ORDER BY tenant_id, created_at DESC`, Select, Select},
		{"postgres", `TABLE public.customers`, Select, Select},
		{"postgres", `WITH scoped AS MATERIALIZED (SELECT id FROM customers WHERE tenant_id=7) SELECT * FROM scoped`, Select, Select},
		{"postgres", `INSERT INTO archive(id) SELECT id FROM customers WHERE tenant_id=7 ON CONFLICT (id) DO NOTHING RETURNING id`, Insert, Insert},
		{"postgres", `UPDATE customers c SET active=false FROM accounts a WHERE a.id=c.account_id AND a.closed=true RETURNING c.id`, Update, Update},
		{"mysql", "SELECT /*+ MAX_EXECUTION_TIME(1000) */ JSON_EXTRACT(profile, '$.name') FROM customers WHERE tenant_id=7 ORDER BY id LIMIT 100", Select, Select},
		{"mysql", `WITH RECURSIVE seq AS (SELECT 1 n UNION ALL SELECT n+1 FROM seq WHERE n<10) SELECT n FROM seq`, Select, Select},
		{"mysql", `INSERT INTO archive(id) SELECT id FROM customers WHERE tenant_id=7 ON DUPLICATE KEY UPDATE id=VALUES(id)`, Insert, Insert},
		{"mysql", `UPDATE customers c JOIN accounts a ON a.id=c.account_id SET c.active=0 WHERE a.closed=1`, Update, Update},
		{"mssql", `SELECT TOP (100) WITH TIES c.id, c.email FROM dbo.customers c WHERE c.tenant_id=7 ORDER BY c.created_at DESC`, Select, Select},
		{"mssql", `;WITH scoped AS (SELECT id FROM dbo.customers WHERE tenant_id=7) SELECT * FROM scoped`, Select, Select},
		{"mssql", `MERGE dbo.target AS t USING dbo.source AS s ON t.id=s.id WHEN MATCHED THEN UPDATE SET t.name=s.name WHEN NOT MATCHED THEN INSERT(id,name) VALUES(s.id,s.name);`, Update, Update},
		{"mssql", `DELETE c OUTPUT deleted.id FROM dbo.customers c JOIN dbo.accounts a ON a.id=c.account_id WHERE a.closed=1`, Delete, Delete},
	}
	for _, test := range tests {
		t.Run(test.engine+"/"+test.sql[:min(12, len(test.sql))], func(t *testing.T) {
			info, err := Parse(test.sql)
			if err != nil || info.Kind != test.kind || info.Command != test.cmd {
				t.Fatalf("got kind=%s command=%s error=%v for %s", info.Kind, info.Command, err, test.sql)
			}
		})
	}
}

func TestRoutineBodiesAreOneStatement(t *testing.T) {
	for _, sql := range []string{
		"CREATE PROCEDURE p(IN x INT)\nBEGIN\n  IF x > 0 THEN\n    INSERT INTO t VALUES (x);\n  END IF;\n  SELECT CASE WHEN x > 1 THEN 'a' ELSE 'b' END;\nEND",
		"CREATE DEFINER=`root`@`%` TRIGGER tr BEFORE INSERT ON t FOR EACH ROW BEGIN SET NEW.a = 1; SET NEW.b = 2; END",
		"CREATE OR ALTER PROCEDURE dbo.p @x int AS\nBEGIN\n  BEGIN TRANSACTION;\n  UPDATE t SET a = @x WHERE id = 1;\n  COMMIT;\nEND",
		"CREATE FUNCTION f() RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN 1; END $$",
	} {
		info, err := Parse(sql)
		if err != nil || info.Kind == Multi {
			t.Fatalf("%q split into several statements: %v %v", sql, info.Kind, err)
		}
	}
	for _, sql := range []string{
		"CREATE PROCEDURE p() BEGIN SELECT 1; END; DROP TABLE t",
		"CREATE TABLE function_log (id int); DROP TABLE t",
		"SELECT 1; SELECT 2",
	} {
		if info, _ := Parse(sql); info.Kind != Multi {
			t.Fatalf("%q was not treated as several statements", sql)
		}
	}
}

func TestHashCommentIsDialectAware(t *testing.T) {
	// On PostgreSQL and SQL Server "#" is not a comment, so a ";" after it is
	// a second statement the parser must see and refuse.
	for _, dialect := range []Dialect{DialectPostgres, DialectMSSQL} {
		info, err := ParseDialect(dialect, "SELECT data #> '{a}' FROM t; DELETE FROM t")
		if err != nil {
			t.Fatalf("%s: %v", dialect, err)
		}
		if info.Kind != Multi {
			t.Fatalf("%s: hid a second statement behind '#', kind=%s", dialect, info.Kind)
		}
		// The "#>" operator on its own is a single readable statement.
		single, err := ParseDialect(dialect, "SELECT data #> '{a}' AS v FROM t")
		if err != nil || single.Kind != Select {
			t.Fatalf("%s: '#>' query misread: kind=%s err=%v", dialect, single.Kind, err)
		}
	}
	// On MySQL and MariaDB "#" starts a comment, so the rest of the line,
	// semicolon and all, is not a second statement.
	info, err := ParseDialect(DialectMySQL, "SELECT 1 AS n # ; DROP TABLE users")
	if err != nil || info.Kind != Select {
		t.Fatalf("mysql: '#' comment mishandled: kind=%s err=%v", info.Kind, err)
	}
	if DialectForEngine("mariadb") != DialectMySQL || DialectForEngine("postgresql") != DialectPostgres || DialectForEngine("sqlserver") != DialectMSSQL {
		t.Fatal("engine to dialect mapping is wrong")
	}
}

// A T-SQL diagnostics script: a table variable is declared, filled from
// constants and joined against the catalog. None of it changes data, so none
// of it may be blocked as unclassifiable.
func TestTSQLDiagnosticsScriptIsClassified(t *testing.T) {
	declare, err := ParseDialect("mssql", "DECLARE @Tables TABLE (SchemaName sysname, TableName sysname)")
	if err != nil || declare.Kind != Session || IsWrite(declare.Kind) {
		t.Fatalf("DECLARE classified as %s (write=%v, err=%v)", declare.Kind, IsWrite(declare.Kind), err)
	}
	fill, err := ParseDialect("mssql", "INSERT INTO @Tables VALUES ('dbo', 'Currency'), ('payment', 'Payment')")
	if err != nil || fill.Kind != Session || fill.Command != Insert {
		t.Fatalf("INSERT into a table variable classified as %s/%s (err=%v)", fill.Kind, fill.Command, err)
	}
	read, err := ParseDialect("mssql", "SELECT x.SchemaName, SUM(ps.row_count) AS Rows FROM @Tables x LEFT JOIN sys.dm_db_partition_stats ps ON ps.object_id = 1 GROUP BY x.SchemaName")
	if err != nil || read.Kind != Select || len(read.Tables) != 1 || read.Tables[0].Name != "dm_db_partition_stats" {
		t.Fatalf("the catalog read classified as %s tables=%v (err=%v)", read.Kind, read.Tables, err)
	}
}

func TestATableVariableDoesNotHideARead(t *testing.T) {
	// The write target is local, but customers is read, so the statement
	// stays a write and keeps its table for masking and row filters.
	info, err := ParseDialect("mssql", "INSERT INTO @rows SELECT id, email FROM customers WHERE id = 1")
	if err != nil || info.Kind != Insert || len(info.Tables) != 1 || info.Tables[0].Name != "customers" {
		t.Fatalf("classified as %s tables=%v (err=%v)", info.Kind, info.Tables, err)
	}
	// A real table keeps its kind even when nothing is read.
	plain, err := ParseDialect("mssql", "INSERT INTO audit_log VALUES (1, 'x')")
	if err != nil || plain.Kind != Insert {
		t.Fatalf("INSERT into a table classified as %s (err=%v)", plain.Kind, err)
	}
}

func TestCursorVerbsAreClassified(t *testing.T) {
	for query, want := range map[string]Kind{
		"DECLARE reader CURSOR FOR SELECT id FROM orders": Session,
		"OPEN reader":            Session,
		"FETCH NEXT FROM reader": Select,
		"CLOSE reader":           Session,
		"DEALLOCATE reader":      Session,
		"PRINT 'done'":           Session,
	} {
		info, err := Parse(query)
		if err != nil || info.Kind != want {
			t.Fatalf("%q classified as %s, want %s (err=%v)", query, info.Kind, want, err)
		}
	}
}

// Statement splitting cuts on semicolons, so a T-SQL block can arrive with a
// write inside it. The block must carry that write's kind, or the write rules
// never see it.
func TestBlocksTakeTheKindOfTheStatementInside(t *testing.T) {
	for query, want := range map[string]Kind{
		"BEGIN TRANSACTION":                                   Session,
		"BEGIN TRY DELETE FROM orders":                        Delete,
		"BEGIN UPDATE orders SET paid = 1":                    Update,
		"IF EXISTS (SELECT 1 FROM orders) DELETE FROM orders": Delete,
		"IF @count > 0 SET @count = 0":                        Session,
		"WHILE @i < 10 INSERT INTO audit VALUES (@i)":         Insert,
		"IF OBJECT_ID('tmp') IS NOT NULL DROP TABLE tmp":      DDL,
		"IF EXISTS (SELECT 1 FROM orders) SELECT 1":           Select,
	} {
		info, err := ParseDialect("mssql", query)
		if err != nil || info.Kind != want {
			t.Fatalf("%q classified as %s, want %s (err=%v)", query, info.Kind, want, err)
		}
	}
	drop, err := ParseDialect("mssql", "IF OBJECT_ID('tmp') IS NOT NULL DROP TABLE tmp")
	if err != nil || !drop.IsDrop {
		t.Fatalf("a DROP inside a block did not set IsDrop (err=%v)", err)
	}
	truncate, err := ParseDialect("mssql", "IF 1 = 1 TRUNCATE TABLE audit")
	if err != nil || !truncate.IsTruncate || truncate.Kind != DDL {
		t.Fatalf("a TRUNCATE inside a block classified as %s IsTruncate=%v (err=%v)", truncate.Kind, truncate.IsTruncate, err)
	}
}

func TestAdministrationStatementsAreWrites(t *testing.T) {
	for _, query := range []string{"BACKUP DATABASE payments TO DISK = 'p.bak'", "RESTORE DATABASE payments FROM DISK = 'p.bak'", "CHECKPOINT", "FLUSH TABLES", "LOCK TABLES orders WRITE"} {
		info, err := Parse(query)
		if err != nil || info.Kind != DDL || !IsWrite(info.Kind) {
			t.Fatalf("%q classified as %s (err=%v)", query, info.Kind, err)
		}
	}
}

// PREPARE hides its statement in a string, so it must stay unclassified and
// leave the decision to the guardrail.
func TestPrepareStaysUnclassified(t *testing.T) {
	info, err := Parse("PREPARE cleanup FROM 'DELETE FROM orders'")
	if err != nil || info.Kind != Other {
		t.Fatalf("PREPARE classified as %s (err=%v)", info.Kind, err)
	}
}

// MySQL runs what is inside /*! ... */; every other engine ignores it. Read
// as a comment it hides whatever it carries from every rule.
func TestMySQLExecutableCommentsAreSQL(t *testing.T) {
	hidden, err := ParseDialect(DialectMySQL, "SELECT 1 /*! ; DROP TABLE users */")
	if err != nil || hidden.Kind != Multi {
		t.Fatalf("a statement hidden in an executable comment classified as %s (err=%v)", hidden.Kind, err)
	}
	versioned, err := ParseDialect(DialectMySQL, "/*!40001 DELETE FROM users */")
	if err != nil || versioned.Kind != Delete || len(versioned.Tables) != 1 || versioned.Tables[0].Name != "users" {
		t.Fatalf("a versioned executable comment classified as %s tables=%v (err=%v)", versioned.Kind, versioned.Tables, err)
	}
	// PostgreSQL and SQL Server treat it as an ordinary comment.
	plain, err := ParseDialect(DialectPostgres, "SELECT 1 /*! ; DROP TABLE users */")
	if err != nil || plain.Kind != Select {
		t.Fatalf("PostgreSQL read an executable comment as SQL: %s (err=%v)", plain.Kind, err)
	}
}

func TestMySQLStringsAndCommentsFollowMySQLRules(t *testing.T) {
	// A backslash escapes the quote, so this is one string, not the start of
	// a second one.
	escaped, err := ParseDialect(DialectMySQL, `SELECT '\'' , 1`)
	if err != nil || escaped.Kind != Select {
		t.Fatalf("an escaped quote classified as %s (err=%v)", escaped.Kind, err)
	}
	// MySQL does not nest block comments: the first */ ends this one.
	nested, err := ParseDialect(DialectMySQL, "/* /* */ DELETE FROM users")
	if err != nil || nested.Kind != Delete {
		t.Fatalf("MySQL nested-comment handling classified as %s (err=%v)", nested.Kind, err)
	}
	// PostgreSQL does nest them, so the same text is one unterminated
	// comment and fails closed.
	if _, err := ParseDialect(DialectPostgres, "/* /* */ DELETE FROM users"); err == nil {
		t.Fatal("PostgreSQL accepted an unterminated nested comment")
	}
}

// SELECT ... INTO writes: a new table on SQL Server and PostgreSQL, a file on
// MySQL. Only SELECT ... INTO @variable stays a read.
func TestSelectIntoIsAWrite(t *testing.T) {
	for dialect, query := range map[Dialect]string{
		DialectMSSQL:    "SELECT * INTO archive_users FROM users",
		DialectPostgres: "SELECT * INTO archive_users FROM users",
		DialectMySQL:    "SELECT id FROM users INTO OUTFILE '/tmp/users.txt'",
	} {
		info, err := ParseDialect(dialect, query)
		if err != nil || info.Kind != DDL || !IsWrite(info.Kind) {
			t.Fatalf("%s: %q classified as %s (err=%v)", dialect, query, info.Kind, err)
		}
		for _, table := range info.Tables {
			if table.Name == "outfile" || table.Name == "dumpfile" {
				t.Fatalf("%q reported the output file as a table: %v", query, info.Tables)
			}
		}
	}
	variable, err := ParseDialect(DialectMSSQL, "SELECT @total = COUNT(*) INTO @rows FROM orders WHERE id = 1")
	if err != nil || variable.Kind != Select {
		t.Fatalf("SELECT INTO a variable classified as %s (err=%v)", variable.Kind, err)
	}
}

// Masking and row filters match on the table name, so a name outside ASCII
// has to survive lexing whole.
func TestNonASCIIIdentifiersStayWhole(t *testing.T) {
	info, err := ParseDialect(DialectPostgres, "SELECT ad FROM müşteri WHERE id = 1")
	if err != nil || len(info.Tables) != 1 || info.Tables[0].Name != "müşteri" {
		t.Fatalf("tables=%v (err=%v)", info.Tables, err)
	}
	quoted, err := ParseDialect(DialectMSSQL, "SELECT * FROM [dbo].[Müşteri] WHERE Id = 1")
	if err != nil || len(quoted.Tables) != 1 || quoted.Tables[0].Schema != "dbo" || quoted.Tables[0].Name != "müşteri" {
		t.Fatalf("tables=%v (err=%v)", quoted.Tables, err)
	}
	japanese, err := ParseDialect(DialectMySQL, "SELECT * FROM 注文 WHERE id = 1")
	if err != nil || len(japanese.Tables) != 1 || japanese.Tables[0].Name != "注文" {
		t.Fatalf("tables=%v (err=%v)", japanese.Tables, err)
	}
}

func TestPostgresTableStatementNamesItsTable(t *testing.T) {
	info, err := ParseDialect(DialectPostgres, "TABLE users")
	if err != nil || info.Kind != Select || len(info.Tables) != 1 || info.Tables[0].Name != "users" {
		t.Fatalf("kind=%s tables=%v (err=%v)", info.Kind, info.Tables, err)
	}
}

// A table that follows FROM or INTO must not be aliased to the next keyword.
func TestKeywordsAreNotAliases(t *testing.T) {
	info, err := ParseDialect(DialectMSSQL, "SELECT * INTO archive FROM users")
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range info.Tables {
		if table.Alias == "from" || table.Alias == "into" {
			t.Fatalf("a keyword became an alias: %v", info.Tables)
		}
	}
}

// EXPLAIN ANALYZE runs the statement it explains on PostgreSQL, so it must
// carry that statement's kind; plain EXPLAIN only plans.
func TestExplainAnalyzeCarriesTheStatementItRuns(t *testing.T) {
	for query, want := range map[string]Kind{
		"EXPLAIN DELETE FROM users":                       Select,
		"EXPLAIN ANALYZE DELETE FROM users":               Delete,
		"EXPLAIN (ANALYZE, BUFFERS) UPDATE users SET a=1": Update,
		"EXPLAIN ANALYZE SELECT * FROM users":             Select,
		"EXPLAIN SELECT * FROM users":                     Select,
	} {
		info, err := ParseDialect(DialectPostgres, query)
		if err != nil || info.Kind != want {
			t.Fatalf("%q classified as %s, want %s (err=%v)", query, info.Kind, want, err)
		}
	}
}

// SET GLOBAL changes the server for everyone, including switching logging
// off, so it is administration rather than session state.
func TestServerWideSetIsAdministration(t *testing.T) {
	for query, want := range map[string]Kind{
		"SET GLOBAL general_log = OFF":     DDL,
		"SET PERSIST max_connections = 10": DDL,
		"SET SESSION sql_mode = ''":        Session,
		"SET autocommit = 1":               Session,
	} {
		info, err := ParseDialect(DialectMySQL, query)
		if err != nil || info.Kind != want {
			t.Fatalf("%q classified as %s, want %s (err=%v)", query, info.Kind, want, err)
		}
	}
}

func TestOnDuplicateKeyUpdateNamesNoSecondTable(t *testing.T) {
	info, err := ParseDialect(DialectMySQL, "INSERT INTO orders VALUES (1) ON DUPLICATE KEY UPDATE total = 1")
	if err != nil || info.Kind != Insert || len(info.Tables) != 1 || info.Tables[0].Name != "orders" {
		t.Fatalf("kind=%s tables=%v (err=%v)", info.Kind, info.Tables, err)
	}
}
