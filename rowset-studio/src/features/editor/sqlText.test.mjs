import { test } from 'node:test';
import assert from 'node:assert/strict';
import { formatSql, hashComments, splitStatements, statementAt, sqlTokens } from './sqlText.ts';

test('format preserves every quoted token and comment', () => {
  const sql = " SELECT 'a  b, WHERE c', \"Case Name\", $$begin;  end$$ FROM t -- keep  this\n WHERE x='it''s  safe' /* nested /* text */ remains */;";
  const protectedTokens = (s) => sqlTokens(s).filter(t => ['quoted','comment'].includes(t.kind)).map(t => t.text);
  assert.deepEqual(protectedTokens(formatSql(sql)), protectedTokens(sql));
  assert.equal(formatSql("SELECT 'a  b';"), "SELECT 'a  b';");
  assert.match(formatSql('SELECT 1; -- comment\nSELECT 2;'), /-- comment\nSELECT 2/);
});
test('split ignores semicolons in strings, dollar bodies, identifiers and comments', () => {
  const sql = "SELECT ';', $$;$$, [a;b]; -- ;\nSELECT 2;";
  assert.equal(splitStatements(sql).length, 2);
  assert.equal(statementAt(sql, 4), "SELECT ';', $$;$$, [a;b];");
  assert.match(statementAt(sql, sql.length), /SELECT 2/);
});

test("routine bodies are not split at their semicolons", () => {
  const mysql = "CREATE PROCEDURE p(IN x INT)\nBEGIN\n  IF x > 0 THEN\n    INSERT INTO t VALUES (x);\n  END IF;\n  SELECT CASE WHEN x > 1 THEN 'a' ELSE 'b' END;\nEND;\nSELECT 1;";
  assert.deepEqual(splitStatements(mysql).map((s) => s.sql), [mysql.slice(0, mysql.indexOf("\nSELECT 1;")), "SELECT 1;"]);
  const mssql = "CREATE OR ALTER TRIGGER tr ON t AFTER INSERT AS\nBEGIN\n  BEGIN TRANSACTION;\n  UPDATE t SET a = 1;\n  COMMIT;\nEND;\nSELECT 2";
  assert.equal(splitStatements(mssql).length, 2);
  assert.equal(splitStatements("SELECT 1; SELECT 2").length, 2);
});

test("'#' is a comment only on MySQL and MariaDB", () => {
  // SQL Server '#temp' tables and PostgreSQL '#>' are not comments.
  // On SQL Server the batch separator is GO, so "#temp" must not swallow the
  // rest of the line and hide the batch that follows it.
  assert.equal(splitStatements("SELECT * INTO #temp FROM orders\nGO\nSELECT * FROM #temp", "mssql").length, 2);
  // Within one batch the semicolons stay where they were written.
  assert.equal(splitStatements("SELECT * INTO #temp FROM orders; SELECT * FROM #temp", "mssql").length, 1);
  assert.equal(splitStatements("SELECT a #> b FROM t; SELECT c FROM t", "postgres").length, 2);
  // On MySQL/MariaDB the rest of the line, ';' and all, is a comment.
  assert.equal(splitStatements("SELECT 1 # a ; b\nFROM t", "mysql").length, 1);
  assert.equal(splitStatements("SELECT 1 # a ; b\nFROM t", "mariadb").length, 1);
  // With no engine the historical behaviour (comment) is kept.
  assert.equal(splitStatements("SELECT 1 # x ; y").length, 1);
  assert.deepEqual([hashComments("mysql"), hashComments("mssql"), hashComments("postgres"), hashComments()], [true, false, false, true]);
});

test('Cassandra batch stays one statement through APPLY BATCH', () => {
  const batch = "BEGIN UNLOGGED BATCH\nINSERT INTO items (id) VALUES (1);\nUPDATE items SET name = 'a;b' WHERE id = 2;\nAPPLY BATCH;";
  assert.deepEqual(splitStatements(`${batch}\nSELECT * FROM items;`, 'cassandra').map((item) => item.sql), [batch, 'SELECT * FROM items;']);
});

test('SQL Server batches split on GO, not on semicolons', () => {
  const script = [
    'SET NOCOUNT ON;',
    '',
    'DECLARE @BatchSize int = 5000;',
    '',
    'WHILE 1 = 1',
    'BEGIN',
    '  ;WITH c AS (SELECT TOP (@BatchSize) Id FROM OrderTrade WHERE Id < 400 ORDER BY Id)',
    '  DELETE FROM c;',
    'END',
  ].join('\n');
  // A DECLARE and the loop that uses its variable belong to one batch; split
  // on ";" they could not see each other.
  assert.equal(splitStatements(script, 'mssql').length, 1);
  assert.equal(splitStatements(script, 'sqlserver').length, 1);
  // Every other engine still splits on the semicolon.
  assert.equal(splitStatements(script, 'postgres').length > 1, true);

  assert.deepEqual(splitStatements('SELECT 1\nGO\nSELECT 2\nGO', 'mssql').map(s => s.sql), ['SELECT 1', 'SELECT 2']);
  // sqlcmd allows a repeat count after GO.
  assert.deepEqual(splitStatements('SELECT 1\nGO 3\nSELECT 2', 'mssql').map(s => s.sql), ['SELECT 1', 'SELECT 2']);
  assert.deepEqual(splitStatements('SELECT 1\ngo\t\nSELECT 2', 'mssql').map(s => s.sql), ['SELECT 1', 'SELECT 2']);
  assert.deepEqual(splitStatements('SELECT 1\nGO -- next batch\nSELECT 2', 'mssql').map(s => s.sql), ['SELECT 1', 'SELECT 2']);
});

test('GO only separates when it stands alone on its line', () => {
  // A column, alias or table called "go" is a name, not a separator.
  assert.equal(splitStatements('SELECT go FROM t WHERE go = 1', 'mssql').length, 1);
  assert.equal(splitStatements('SELECT 1 AS go', 'mssql').length, 1);
  assert.equal(splitStatements('SELECT 1 GO', 'mssql').length, 1);
  assert.equal(splitStatements('SELECT 1\nGO SELECT 2', 'mssql').length, 1);
  // Inside a string or a comment it is text.
  assert.equal(splitStatements("SELECT '\nGO\n' AS v", 'mssql').length, 1);
  assert.equal(splitStatements('SELECT 1 /*\nGO\n*/', 'mssql').length, 1);
  // A trailing GO leaves no empty batch behind.
  assert.deepEqual(splitStatements('SELECT 1\nGO\n', 'mssql').map(s => s.sql), ['SELECT 1']);
  assert.deepEqual(splitStatements('\nGO\n', 'mssql').map(s => s.sql), []);
});
