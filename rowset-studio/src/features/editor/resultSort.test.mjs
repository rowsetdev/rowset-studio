import assert from 'node:assert/strict';
import { test } from 'node:test';
import { compareCells, sortRowIndexes } from './resultSort.ts';

test('large integers and exact decimals sort without floating point conversion', () => {
  assert(compareCells('9007199254740993', '9007199254740992', 'BIGINT') > 0);
  assert(compareCells('0.12345678901234567891', '0.12345678901234567890', 'NUMERIC') > 0);
  assert(compareCells('-9007199254740993', '-9007199254740992', 'BIGINT') < 0);
  assert.equal(compareCells('01.00', '1e0', 'DECIMAL'), 0);
  assert(compareCells('1e999999999', '9e999999998', 'NUMERIC') > 0);
  assert(compareCells('-0.0001', '0', 'NUMERIC') < 0);
  assert.equal(compareCells('-0', '0.00', 'NUMERIC'), 0);
});
test('NULL stays last and text IDs stay textual', () => {
  assert(compareCells(null, 1, 'INT', 'desc') > 0);
  assert(compareCells(1, undefined, 'INT', 'asc') < 0);
  assert(compareCells('10', '2', 'VARCHAR') < 0);
  assert(compareCells('10', '2', 'INT', 'desc') < 0);
  assert(compareCells('', '0', 'INT') < 0);
});

test('prepared sort keys preserve exact decimal order and NULL placement', () => {
  const rows = [['9007199254740993'], [null], ['-0.0001'], ['1e999999999'], ['9007199254740992'], ['0'], ['abc']];
  const indexes = rows.map((_, index) => index);
  for (const direction of ['asc', 'desc']) {
    const expected = [...indexes].sort((a, b) => compareCells(rows[a][0], rows[b][0], 'NUMERIC', direction));
    assert.deepEqual(sortRowIndexes(rows, indexes, 0, 'NUMERIC', direction), expected);
  }
});

test('prepared sorting only the filtered row indexes preserves source rows', () => {
  const rows = [[9], [1], [7], [null], [3]];
  const indexes = [4, 0, 3];
  assert.deepEqual(sortRowIndexes(rows, indexes, 0, 'INTEGER'), [4, 0, 3]);
  assert.deepEqual(indexes, [4, 0, 3]);
});
