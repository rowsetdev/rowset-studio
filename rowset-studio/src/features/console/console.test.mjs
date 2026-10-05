import test from 'node:test';
import assert from 'node:assert/strict';
import { pending, elasticsearchBody, footer, promptLabel } from './consoleStatement.ts';
import { engineSpec, consoleEngines } from './consoleEngines.ts';
import { matcher, metaActions } from './consoleMeta.ts';
import { matchCommand } from './consoleEngines.ts';
import { redisPlan, tokens } from './consoleRedis.ts';
import { grid, displayWidth, padCell, fitCell, records, cellKind } from './consoleFormat.ts';

const postgres = engineSpec('postgres');
const mssql = engineSpec('mssql');
const mongo = engineSpec('mongodb');
const redis = engineSpec('redis');

test('a statement runs only once its terminator arrives outside quotes and brackets', () => {
  assert.equal(pending(postgres, ['select 1']), undefined);
  assert.equal(pending(postgres, ['select 1;']).statement, 'select 1');
  assert.equal(pending(postgres, ['select', '  1;']).statement, 'select\n  1');
  // A semicolon inside a string or a comment does not end anything.
  assert.equal(pending(postgres, ["select ';'"]), undefined);
  assert.equal(pending(postgres, ['select 1 -- ;']), undefined);
  assert.equal(pending(postgres, ['select 1 /* ; */']), undefined);
  assert.equal(pending(postgres, ['select 1 /* x', 'y */;']).statement.includes('select 1'), true);
  // An open bracket keeps the buffer open even with a semicolon in it.
  assert.equal(pending(postgres, ['select (1;']), undefined);
  assert.equal(pending(postgres, ["select 'it''s ok';"]).statement, "select 'it''s ok'");
  assert.equal(pending(postgres, ['']), undefined);
});

test('mysql \\G ends a statement and asks for that one result expanded', () => {
  const mysql = engineSpec('mysql');
  const result = pending(mysql, ['select 1 \\G']);
  assert.equal(result.statement, 'select 1');
  assert.equal(result.expandedOnce, true);
  assert.equal(pending(mysql, ['select 1;']).expandedOnce, false);
});

test('sqlcmd runs a batch on a GO line of its own, which is not part of it', () => {
  assert.equal(pending(mssql, ['select 1']), undefined);
  assert.equal(pending(mssql, ['select 1;']), undefined);
  assert.equal(pending(mssql, ['select 1', 'GO']).statement, 'select 1');
  assert.equal(pending(mssql, ['select 1', 'go']).statement, 'select 1');
  assert.equal(pending(mssql, ['GO']), undefined);
});

test('an engine with no terminator runs a line at a time but waits for a closing brace', () => {
  assert.equal(pending(mongo, ['db.users.find({})']).statement, 'db.users.find({})');
  assert.equal(pending(mongo, ['db.users.find({']), undefined);
  assert.equal(pending(mongo, ['db.users.find({', '"a":1})']).statement, 'db.users.find({\n"a":1})');
  assert.equal(pending(redis, ['keys *']).statement, 'keys *');
});

test('a meta command needs no terminator', () => {
  assert.equal(pending(postgres, ['\\dt']).statement, '\\dt');
  assert.equal(pending(mssql, [':help']).statement, ':help');
});

test('the longest command name wins, so describe tables is not read as describe', () => {
  const cassandra = engineSpec('cassandra');
  assert.equal(matchCommand(cassandra, 'describe tables').command.action, 'tables');
  assert.equal(matchCommand(cassandra, 'describe keyspaces').command.action, 'databases');
  assert.equal(matchCommand(cassandra, 'describe orders').command.action, 'describe');
  assert.equal(matchCommand(cassandra, 'describe orders').argument, 'orders');
  assert.equal(matchCommand(mongo, 'show collections').command.action, 'tables');
  assert.equal(matchCommand(mongo, 'show dbs').command.action, 'databases');
});

test('a command name is a whole word, so a column called use or select is still SQL', () => {
  assert.equal(matchCommand(engineSpec('mysql'), 'select user_id from t'), undefined);
  assert.equal(matchCommand(engineSpec('mysql'), 'usez'), undefined);
  assert.equal(matchCommand(engineSpec('mysql'), 'use shop').argument, 'shop');
  assert.equal(matchCommand(redis, 'select 3').argument, '3');
});

test('every engine Rowset supports has a console, and every command has an implementation', () => {
  for (const name of ['postgres', 'mysql', 'mariadb', 'mssql', 'cockroachdb', 'sqlite', 'duckdb', 'clickhouse', 'mongodb', 'redis', 'valkey', 'cassandra', 'elasticsearch']) {
    assert.ok(engineSpec(name), `${name} has no console spec`);
  }
  for (const [name, spec] of Object.entries(consoleEngines)) {
    for (const command of spec.commands) {
      assert.ok(metaActions[command.action], `${name} maps ${command.name} to a missing action ${command.action}`);
    }
  }
});

test('a pattern matches as a wildcard and as a substring', () => {
  assert.equal(matcher('ord*')('orders'), true);
  assert.equal(matcher('%ord%')('my_orders'), true);
  assert.equal(matcher('ord')('my_orders'), true);
  assert.equal(matcher('ord')('customers'), false);
  assert.equal(matcher('')('anything'), true);
  // A pattern is text, not a regular expression.
  assert.equal(matcher('a.b')('axb'), false);
  assert.equal(matcher('a.b')('a.b'), true);
});

test('redis-cli commands become scan, write or delete', () => {
  assert.deepEqual(redisPlan('keys user:*'), { kind: 'scan', pattern: 'user:*', type: undefined, limit: undefined });
  assert.deepEqual(redisPlan('scan 0 match user:* count 50'), { kind: 'scan', pattern: 'user:*', type: undefined, limit: 50 });
  assert.equal(redisPlan('get greeting').columns.join(','), 'key,value');
  assert.equal(redisPlan('ttl greeting').columns.join(','), 'key,ttl');
  assert.deepEqual(redisPlan('set greeting "hello world" EX 60'), { kind: 'write', key: 'greeting', type: 'string', value: 'hello world', ttlSeconds: 60 });
  assert.deepEqual(redisPlan('setex greeting 30 hi'), { kind: 'write', key: 'greeting', type: 'string', value: 'hi', ttlSeconds: 30 });
  assert.deepEqual(redisPlan('hset user:1 name ada'), { kind: 'write', key: 'user:1', type: 'hash', field: 'name', value: 'ada' });
  assert.deepEqual(redisPlan('del user:1'), { kind: 'delete', key: 'user:1' });
  assert.equal(redisPlan('del a b').kind, 'error');
  assert.equal(redisPlan('flushall').kind, 'error');
  assert.equal(redisPlan('get').kind, 'error');
  assert.deepEqual(tokens('set k "a b" EX 1'), ['set', 'k', 'a b', 'EX', '1']);
  assert.deepEqual(tokens('set k ""'), ['set', 'k', '']);
});

test('a search accepts the Kibana form and falls back to the selected index', () => {
  assert.deepEqual(JSON.parse(elasticsearchBody('GET /orders/_search {"query":{"match_all":{}}}', 'other')), { query: { match_all: {} }, index: 'orders' });
  assert.deepEqual(JSON.parse(elasticsearchBody('{"query":{"match_all":{}}}', 'orders')), { query: { match_all: {} }, index: 'orders' });
  assert.deepEqual(JSON.parse(elasticsearchBody('{"index":"explicit","size":5}', 'orders')), { index: 'explicit', size: 5 });
  assert.throws(() => elasticsearchBody('not json', 'orders'));
});

test('columns are measured for alignment, wide characters included', () => {
  const g = grid(['id', 'name'], [[1, 'ada'], [2, null]]);
  assert.deepEqual(g.widths, [2, 4]);
  assert.deepEqual(g.numeric, [true, false]);
  assert.equal(g.rows[1][1].text, 'NULL');
  assert.equal(g.rows[1][1].kind, 'null');
  assert.equal(displayWidth('한글'), 4);
  assert.equal(displayWidth('ab'), 2);
  assert.equal(padCell('ab', 4, false), 'ab  ');
  assert.equal(padCell('7', 3, true), '  7');
  assert.equal(fitCell('abcdef', 4), 'abc…');
  assert.equal(displayWidth(padCell('한글', 6, false)), 6);
  // A column of numbers with one text value in it is not a numeric column.
  assert.deepEqual(grid(['v'], [[1], ['x']]).numeric, [false]);
  assert.deepEqual(grid(['v'], [[null]]).numeric, [false]);
  assert.equal(cellKind({ a: 1 }), 'json');
});

test('expanded output lists one field per line for each row', () => {
  const out = records(['id', 'name'], [[1, 'ada']]);
  assert.equal(out.length, 1);
  assert.equal(out[0].index, 1);
  assert.deepEqual(out[0].fields.map(f => `${f.name}=${f.cell.text}`), ['id=1', 'name=ada']);
});

test('the footer shows a count, and a duration only when timing is on', () => {
  assert.equal(footer(1, 5, false), '1 row');
  assert.equal(footer(12, 5, true), '12 rows (5 ms)');
  assert.equal(footer(undefined, 1500, true), '(1.500 s)');
});

test('the prompt names a file database by its file name, never its whole path', () => {
  const sqlite = engineSpec('sqlite');
  assert.equal(promptLabel(sqlite, '/var/tmp/a/very/long/path/probe.sqlite'), 'probe.sqlite');
  assert.equal(promptLabel(sqlite, 'C:\\Users\\me\\probe.duckdb'), 'probe.duckdb');
  assert.equal(promptLabel(sqlite, ''), 'sqlite');
  assert.equal(promptLabel(postgres, 'shop'), 'shop');
  assert.equal(promptLabel(postgres, 'x'.repeat(60)).length, 40);
});
