import test from 'node:test';
import assert from 'node:assert/strict';
import { mongoQuery, mongoRequest, formatMongoQuery, mongoShellToRequest, isMongoShellQuery, mongoShellToParts, mongoPartsToShell, mongoRequestToParts, mongoSourceToParts, isMongoAggregateQuery, mongoAggregateRequest, mongoAggregateRequestToParts, mongoAggregateSourceToParts } from './mongoQuery.ts';
test('MongoDB request and formatting preserve raw integers and decimals', () => {
  const source = '{"collection":"items","filter":{"count":9007199254740993,"n":1.12345678901234567890,"text":"a, {b}: c"},"sort":{},"limit":100}';
  assert(mongoRequest(source,'test').includes('9007199254740993'));
  assert(mongoRequest(source,'test').includes('1.12345678901234567890'));
  const formatted = formatMongoQuery(source);
  assert(formatted.includes('9007199254740993'));
  assert(formatted.includes('1.12345678901234567890'));
  assert.deepEqual(JSON.parse(formatted),JSON.parse(source));
  assert.equal(JSON.parse(mongoRequest(mongoQuery('items'),'test')).database,'test');
});
test('MongoDB invalid queries fail locally instead of silently dropping options', () => {
  for (const source of ['[]','{}','{"collection":"items","pipeline":[]}','{"collection":"items","filter":[]}','{"collection":"items","limit":-1}','{"collection":"items","database":"other"}']) assert.throws(()=>mongoRequest(source,'test'));
});
test('MongoDB shell syntax db.collection.find(...) translates to the request JSON', () => {
  assert.equal(isMongoShellQuery('db.customers.find({})'), true);
  assert.equal(isMongoShellQuery('{"collection":"customers"}'), false);
  assert.deepEqual(JSON.parse(mongoShellToRequest('db.customers.find({})')), { collection: 'customers', filter: {}, sort: {}, limit: 100 });
  assert.deepEqual(JSON.parse(mongoShellToRequest('db.customers.find({"age":{"$gt":21}}).sort({"age":-1}).limit(10)')), { collection: 'customers', filter: { age: { $gt: 21 } }, sort: { age: -1 }, limit: 10 });
  const request = JSON.parse(mongoRequest('db.customers.find({"name":"a, {b}"})', 'test'));
  assert.equal(request.collection, 'customers');
  assert.equal(request.database, 'test');
  assert.deepEqual(JSON.parse(mongoShellToRequest('db.customers.find({}, {"name":1})')), { collection: 'customers', filter: {}, sort: {}, limit: 100, project: { name: 1 } });
  assert.deepEqual(JSON.parse(mongoShellToRequest('db.customers.find({}).skip(5)')), { collection: 'customers', filter: {}, sort: {}, limit: 100, skip: 5 });
  assert.throws(() => mongoShellToRequest('not a query'));
});
test('the query bar round-trips through shell syntax, project, skip and max time included', () => {
  const parts = mongoShellToParts('db.customers.find({"age":{"$gt":21}}, {"name":1}).sort({"age":-1}).skip(5).limit(10).maxTimeMS(2000)');
  assert.deepEqual(parts, { collection: 'customers', filter: '{"age":{"$gt":21}}', project: '{"name":1}', sort: '{"age":-1}', skip: '5', limit: '10', maxTimeMs: '2000' });
  assert.equal(mongoPartsToShell(parts), 'db.customers.find({"age":{"$gt":21}}, {"name":1}).sort({"age":-1}).skip(5).limit(10).maxTimeMS(2000)');
  assert.equal(mongoPartsToShell({ collection: 'orders', filter: '{}', project: '{}', sort: '{}', skip: '0', limit: '100', maxTimeMs: '0' }), 'db.orders.find({})');
  const request = JSON.parse(mongoRequest(mongoPartsToShell(parts), 'test'));
  assert.equal(request.skip, 5);
  assert.equal(request.maxTimeMs, 2000);
  assert.deepEqual(request.project, { name: 1 });
});
test('the query bar also parses the raw request JSON saved in Activity/History, digits intact', () => {
  const saved = '{"database":"rowset_demo","collection":"customers","filter":{},"project":{"status":1,"name":1},"sort":{},"skip":1,"limit":3,"maxTimeMs":5000}';
  const parts = mongoRequestToParts(saved);
  assert.deepEqual(parts, { collection: 'customers', filter: '{}', project: '{"status":1,"name":1}', sort: '{}', skip: '1', limit: '3', maxTimeMs: '5000' });
  assert.deepEqual(mongoSourceToParts(saved), parts);
  assert.deepEqual(mongoSourceToParts('db.customers.find({})'), mongoShellToParts('db.customers.find({})'));
  const big = '{"collection":"items","filter":{"id":9007199254740993},"limit":10}';
  assert.equal(mongoRequestToParts(big).filter, '{"id":9007199254740993}');
  assert.throws(() => mongoRequestToParts('{"filter":{}}'));
});
test('a find() saved with maxTimeMs:0 (the backend "no limit" sentinel) reopens instead of erroring', () => {
  // Reproduces a real bug: the raw request Activity/History save always
  // includes maxTimeMs, and the backend's own zero-value for "unset" was
  // being rejected as out of range on reopen.
  const saved = '{"database":"rowset_ui_check","collection":"ui_probe3","filter":{},"project":null,"sort":{},"skip":0,"limit":100,"maxTimeMs":0}';
  assert.equal(JSON.parse(mongoRequest(saved, 'rowset_ui_check')).maxTimeMs, 0);
});
test('aggregate() reopens from its raw request JSON the same way find() does', () => {
  const saved = '{"database":"rowset_ui_check","collection":"orders","pipeline":[{"$match":{"status":"paid"}}],"maxTimeMs":0,"limit":100}';
  assert.equal(isMongoAggregateQuery(saved), true);
  assert.equal(isMongoAggregateQuery('{"collection":"items","filter":{}}'), false);
  const request = JSON.parse(mongoAggregateRequest(saved, 'rowset_ui_check'));
  assert.equal(request.collection, 'orders');
  assert.equal(request.maxTimeMs, 0);
  assert.deepEqual(request.pipeline, [{ $match: { status: 'paid' } }]);
  const parts = mongoAggregateRequestToParts(saved);
  assert.equal(parts.collection, 'orders');
  assert.deepEqual(mongoAggregateSourceToParts(saved), parts);
  assert.throws(() => mongoAggregateRequest('{"collection":"items","pipeline":[],"unknownField":1}', 'test'));
});
