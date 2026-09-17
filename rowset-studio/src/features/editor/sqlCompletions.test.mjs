import assert from "node:assert/strict";
import test from "node:test";
import { aliasMap, buildSqlCompletions, clauseAt, columnSuggestions, completionName, dotSuggestions, groupByColumns, joinSuggestions } from "./sqlCompletions.ts";

const schema = buildSqlCompletions({ schemas: [{ name: "Public", tables: [{ name: "Users", columns: [{ name: "id", dataType: "int", nullable: false }, { name: "email", dataType: "text", nullable: false }] }], views: [], routines: [], triggers: [] }] });

test("schema and qualified table names feed completion", () => {
  assert.deepEqual(dotSuggestions("select Public.", "public", schema), { kind: "table", values: ["Users"] });
  assert.deepEqual(dotSuggestions("select u. from Public.Users as u", "u", schema), { kind: "column", values: ["id", "email"], owner: "Public.Users" });
});

test("aliases do not consume SQL clause keywords", () => {
  const aliases = aliasMap("select users. from Public.Users WHERE id = 1");
  assert.equal(aliases.users, "public.users");
  assert.equal(aliases.where, undefined);
});

test('database names are offered where a statement can reach them', () => {
  const schema = { schemas: [{ name: 'dbo', tables: [{ name: 'orders', columns: [{ name: 'id' }] }] }] };
  const mssql = buildSqlCompletions(schema, ['rowset_e2e', 'master'], 'sqlserver');
  assert.deepEqual(Object.values(mssql.databaseNames), ['rowset_e2e', 'master']);
  assert.deepEqual(dotSuggestions('select * from rowset_e2e.', 'rowset_e2e', mssql), { kind: 'schema', values: ['dbo'] });
  assert.deepEqual(buildSqlCompletions(schema, ['other'], 'postgres').databaseNames, {});
});

test('SQL Server completion quotes identifiers and uses loaded database schemas', () => {
  const remote = { schemas: [{ name: 'dbo', tables: [{ name: 'Order', columns: [{ name: 'Order ID' }] }], views: [], routines: [], triggers: [] }] };
  const mssql = buildSqlCompletions(undefined, ['BitciExchangeDB'], 'sqlserver', { BitciExchangeDB: remote });
  assert.equal(completionName('dbo.Order', 'sqlserver'), '[dbo].[Order]');
  assert.deepEqual(dotSuggestions('select * from BitciExchangeDB.', 'BitciExchangeDB', mssql), { kind: 'schema', values: ['dbo'] });
  assert.deepEqual(dotSuggestions('select * from BitciExchangeDB.dbo.', 'BitciExchangeDB.dbo', mssql), { kind: 'table', values: ['Order'] });
  assert.deepEqual(dotSuggestions('select BitciExchangeDB.dbo.Order.', 'BitciExchangeDB.dbo.Order', mssql), { kind: 'column', values: ['Order ID'], owner: 'BitciExchangeDB.dbo.Order' });
  assert.equal(aliasMap('select * from BitciExchangeDB.dbo.Order o').o, 'bitciexchangedb.dbo.order');
  assert.deepEqual(columnSuggestions('select o. from BitciExchangeDB.dbo.Order o', mssql).map((item) => item.label), ['Order ID']);
});

test('completion quotes names with every supported SQL dialect', () => {
  for (const engine of ['postgres', 'postgresql', 'cockroachdb', 'sqlite', 'duckdb', 'clickhouse', 'cassandra']) {
    assert.equal(completionName('Sales.Order "new"', engine), '"Sales"."Order ""new"""');
  }
  for (const engine of ['mysql', 'mariadb']) {
    assert.equal(completionName('Sales.Or`der', engine), '`Sales`.`Or``der`');
  }
  for (const engine of ['sqlserver', 'mssql']) {
    assert.equal(completionName('Sales.Or]der', engine), '[Sales].[Or]]der]');
  }
  for (const engine of ['mongodb', 'redis', 'valkey', 'elasticsearch']) {
    assert.equal(completionName('raw.name', engine), 'raw.name');
  }
});

test('two-part database engines complete tables directly', () => {
  const remote = { schemas: [{ name: 'shop', tables: [{ name: 'Order', columns: [{ name: 'id' }] }] }] };
  for (const engine of ['mysql', 'mariadb', 'clickhouse', 'cassandra']) {
    const completions = buildSqlCompletions(undefined, ['shop'], engine, { shop: remote });
    assert.deepEqual(dotSuggestions('select * from shop.', 'shop', completions), { kind: 'table', values: ['Order'] });
    assert.deepEqual(dotSuggestions('select shop.Order.', 'shop.Order', completions), { kind: 'column', values: ['id'], owner: 'shop.Order' });
  }
});

const related = buildSqlCompletions({
  schemas: [{
    name: "public",
    tables: [
      { name: "orders", columns: [{ name: "id", dataType: "int" }, { name: "customer_id", dataType: "int", references: "public.customers.id" }] },
      { name: "customers", columns: [{ name: "id", dataType: "int" }, { name: "email", dataType: "text" }] },
    ],
    views: [], routines: [], triggers: [],
  }],
});

test("the clause under the cursor is read, ignoring subqueries", () => {
  const sql = "SELECT id FROM orders WHERE id IN (SELECT id FROM customers) AND ";
  assert.equal(clauseAt(sql, 7), "select");
  assert.equal(clauseAt(sql, sql.indexOf("orders") + 3), "from");
  assert.equal(clauseAt(sql, sql.length), "where");
  assert.equal(clauseAt("SELECT a, b", 10), "select");
  assert.equal(clauseAt("SELECT 1; INSERT INTO t ", 24), "from");
});

test("joins are offered from the foreign keys of the tables in the statement", () => {
  const joins = joinSuggestions("SELECT * FROM orders o", related);
  assert.equal(joins.length, 1);
  assert.equal(joins[0].insertText, "JOIN public.customers c ON c.id = o.customer_id");
  // Once both tables are there, nothing is left to offer.
  assert.deepEqual(joinSuggestions("SELECT * FROM orders o JOIN public.customers c ON c.id = o.customer_id", related), []);
});

test("columns of several tables are offered through their alias", () => {
  const got = columnSuggestions("SELECT  FROM orders o JOIN public.customers c ON c.id = o.customer_id", related);
  const labels = got.map((item) => item.label).sort();
  // id exists in both tables, so only the qualified forms are offered.
  assert.ok(labels.includes("o.id") && labels.includes("c.id"));
  assert.ok(!labels.includes("id"));
  // customer_id belongs to one table, so the bare name stays available.
  assert.ok(labels.includes("customer_id") && labels.includes("o.customer_id"));
  assert.equal(got.find((item) => item.label === "c.email").qualifiedOnly, false);
});

test("a single table keeps its columns unqualified", () => {
  const got = columnSuggestions("SELECT  FROM orders o", related);
  assert.deepEqual(got.map((item) => item.label).sort(), ["customer_id", "id"]);
  assert.ok(got.every((item) => item.qualifiedOnly === false));
});

test("GROUP BY takes the selected columns that are not aggregates", () => {
  assert.deepEqual(groupByColumns("SELECT customer_id, count(*) FROM orders"), ["customer_id"]);
  assert.deepEqual(groupByColumns("SELECT o.customer_id AS cid, sum(o.total) FROM orders o"), ["o.customer_id"]);
  assert.deepEqual(groupByColumns("SELECT date_trunc('day', created_at) day, count(*) FROM orders"), ["date_trunc('day',created_at)"]);
  assert.deepEqual(groupByColumns("SELECT * FROM orders"), []);
});
