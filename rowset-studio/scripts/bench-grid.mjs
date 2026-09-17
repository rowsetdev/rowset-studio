/* global console, process */
import { performance } from "node:perf_hooks";
import { filteredIndexes } from "../src/features/editor/resultFilter.ts";
import { compareCells, sortRowIndexes } from "../src/features/editor/resultSort.ts";

const count = Number(process.argv[2] || 100000);
const rows = Array.from({ length: count }, (_, i) => { const value = (i * 48271) % 100003; return [value, `customer-${String(value % 20000).padStart(5, "0")}`, `group-${value % 17}`, i % 2 ? null : `note-${value}`]; });
function measure(name, task) {
  const runs = [];
  let result;
  for (let i = 0; i < 4; i++) {
    const start = performance.now();
    result = task();
    runs.push(Math.round(performance.now() - start));
  }
  console.log(`${name}: median ${runs.slice(1).sort((a, b) => a - b)[1]} ms; result ${result.length}`);
}
measure("all rows", () => filteredIndexes(rows, []));
measure("text filter", () => filteredIndexes(rows, [{ column: 1, operator: "contains", value: "999" }]));
measure("numeric sort baseline", () => filteredIndexes(rows, []).sort((a, b) => compareCells(rows[a][0], rows[b][0], "INTEGER", "desc")));
measure("numeric sort prepared", () => sortRowIndexes(rows, filteredIndexes(rows, []), 0, "INTEGER", "desc"));
