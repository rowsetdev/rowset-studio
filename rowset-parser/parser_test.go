package parser

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
