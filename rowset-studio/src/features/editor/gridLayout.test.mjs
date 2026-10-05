import test from 'node:test';
import assert from 'node:assert/strict';
import { autoColumnWidths, densities, formatXml, isXmlColumn, maxColumnWidth, minColumnWidth, measureText } from './gridLayout.ts';

test('a column is only as wide as what it holds', () => {
  const columns = ['id', 'session_id', 'login_name'];
  const rows = [[1, 220, 'NT AUTHORITY\\NETWORK SERVICE'], [2, 1096, 'app_event']];
  const widths = autoColumnWidths(columns, rows, ['INT', 'SMALLINT', 'NVARCHAR']);
  // Narrow data gets a narrow column; the old grid gave every one of these 120px.
  assert.ok(widths[0] < 80, `id column ${widths[0]}`);
  assert.ok(widths[1] < 100, `session_id column ${widths[1]}`);
  assert.ok(widths[2] > widths[1], 'a long value gets more room than a short one');
  for (const width of widths) {
    assert.ok(width >= minColumnWidth && width <= maxColumnWidth, `width ${width} out of range`);
  }
});

test('one very long value cannot push the other columns off screen', () => {
  const [width] = autoColumnWidths(['x'], [['y'.repeat(100000)]]);
  assert.equal(width, maxColumnWidth);
});

test('a column with no rows is still wide enough for its name', () => {
  const [short, long] = autoColumnWidths(['id', 'a_rather_long_column_name'], []);
  assert.ok(long > short);
  assert.ok(short >= minColumnWidth);
});

test('the roomier density leaves space for the type beside the name', () => {
  const compact = autoColumnWidths(['id'], [[1]], ['SMALLINT'], 'compact');
  const roomy = autoColumnWidths(['id'], [[1]], ['SMALLINT'], 'comfortable');
  assert.ok(roomy[0] > compact[0]);
  assert.ok(densities.compact.rowHeight < densities.comfortable.rowHeight);
  assert.equal(densities.compact.showTypes, false);
  assert.equal(densities.comfortable.showTypes, true);
});

test('NULL and objects are measured, not crashed on', () => {
  assert.equal(measureText(null), 4);
  assert.equal(measureText(undefined), 4);
  assert.equal(measureText({ a: 1 }), '{"a":1}'.length);
  assert.doesNotThrow(() => autoColumnWidths(['a', 'b'], [[null, { x: 1 }]]));
});

test('XML is recognised by its column type or by its text', () => {
  assert.equal(isXmlColumn('XML', null), true);
  assert.equal(isXmlColumn('xml', null), true);
  assert.equal(isXmlColumn('NVARCHAR', '<?query -- WITH c AS (...) ?>'), true);
  assert.equal(isXmlColumn('NVARCHAR', '<root><a/></root>'), true);
  assert.equal(isXmlColumn('NVARCHAR', 'plain text'), false);
  assert.equal(isXmlColumn('NVARCHAR', '5 < 7'), false);
  assert.equal(isXmlColumn(undefined, null), false);
  assert.equal(isXmlColumn(undefined, 42), false);
});

test('XML is laid out one element per line without changing its text', () => {
  const out = formatXml('<root><a id="1">x</a><b/></root>');
  assert.deepEqual(out.split('\n'), ['<root>', '  <a id="1">', '    x', '  </a>', '  <b/>', '</root>']);
  // A declaration and a comment open nothing.
  assert.equal(formatXml('<?xml version="1.0"?><r/>').split('\n').length, 2);
  // The content itself is untouched: every tag that went in comes back out.
  const source = '<a><b attr="x &amp; y">text</b></a>';
  assert.equal(formatXml(source).replace(/\s+/g, ''), source.replace(/\s+/g, ''));
  assert.equal(formatXml(''), '');
  assert.equal(formatXml('   '), '');
});
