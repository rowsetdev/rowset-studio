import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readQueryStream } from './queryStream.ts';
const { Response } = globalThis;

/** One NDJSON response body built from the frames a server would send. */
function streamOf(frames) {
  return new Response(frames.map(frame => JSON.stringify(frame)).join('\n'));
}

test('query stream preserves exact values and reports progress and completion', async () => {
  const records=[{type:'columns',columns:['id'],columnTypes:['INT8']},{type:'rows',rows:[['9007199254740993']]},{type:'complete',rowCount:1,durationMs:42,truncated:true}];
  const seen=[];
  const result=await readQueryStream(new Response(records.map(r=>JSON.stringify(r)).join('\n')),r=>seen.push(r));
  assert.equal(result.rows[0][0],'9007199254740993');assert.equal(result.durationMs,42);assert.equal(seen.length,1);assert.equal(result.truncated,true);
});
test('malformed rows and inconsistent completion counts are rejected', async () => {
  await assert.rejects(readQueryStream(new Response('{"type":"columns","columns":["id"]}\n{"type":"rows","rows":[[1,2]]}\n')),/Invalid query row batch/);
  await assert.rejects(readQueryStream(new Response('{"type":"columns","columns":[]}\n{"type":"complete","rowCount":5,"durationMs":1}\n')),/Invalid query completion/);
});
test('a large result is streamed rather than cut off by a hidden size limit', async () => {
  const rows = Array.from({ length: 5000 }, (_, i) => `[${i},"${'x'.repeat(400)}"]`).join(',');
  const body = `{"type":"columns","columns":["id","payload"]}\n{"type":"rows","rows":[${rows}]}\n{"type":"complete","rowCount":5000,"durationMs":3}\n`;
  const result = await readQueryStream(new Response(body));
  assert.equal(result.rows.length, 5000);
  assert.equal(result.rowCount, 5000);
});
test('interrupted and failed streams are not reported as successful', async () => {
  await assert.rejects(readQueryStream(new Response('{"type":"columns","columns":[]}\n')),/interrupted/);
  await assert.rejects(readQueryStream(new Response('{"type":"columns","columns":[]}\n{"type":"complete","error":"database timeout"}\n')),/database timeout/);
});

test('a batch that returns several results keeps every one of them', async () => {
  const frames = [
    { type: 'columns', columns: ['first_set'], columnTypes: ['INT'], node: { host: 'h' } },
    { type: 'rows', rows: [[1]] },
    { type: 'complete', rowCount: 1, durationMs: 3, truncated: false },
    { type: 'result', columns: ['a', 'b'], columnTypes: ['INT', 'INT'] },
    { type: 'rows', rows: [[2, 3], [4, 5]] },
    { type: 'complete', rowCount: 2, durationMs: 7, truncated: false },
  ];
  const result = await readQueryStream(streamOf(frames));
  assert.deepEqual(result.columns, ['first_set']);
  assert.deepEqual(result.rows, [[1]]);
  assert.equal(result.rowCount, 1);
  // The annotations belong to the statement, not to one of its results.
  assert.deepEqual(result.annotations, { node: { host: 'h' } });
  assert.equal(result.more.length, 1);
  assert.deepEqual(result.more[0].columns, ['a', 'b']);
  assert.deepEqual(result.more[0].rows, [[2, 3], [4, 5]]);
  assert.equal(result.more[0].rowCount, 2);
  assert.equal(result.more[0].durationMs, 7);
});

test('one result leaves nothing extra behind', async () => {
  const result = await readQueryStream(streamOf([
    { type: 'columns', columns: ['n'] },
    { type: 'rows', rows: [[1]] },
    { type: 'complete', rowCount: 1, durationMs: 1, truncated: false },
  ]));
  assert.equal(result.more, undefined);
});

test('a further result cannot arrive before the first one finished', async () => {
  await assert.rejects(readQueryStream(streamOf([
    { type: 'columns', columns: ['n'] },
    { type: 'rows', rows: [[1]] },
    { type: 'result', columns: ['a'] },
  ])));
  // Nor rows after a result finished and before the next announces itself.
  await assert.rejects(readQueryStream(streamOf([
    { type: 'columns', columns: ['n'] },
    { type: 'complete', rowCount: 0, durationMs: 1, truncated: false },
    { type: 'rows', rows: [[1]] },
  ])));
});
