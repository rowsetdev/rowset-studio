import { useEffect, useMemo, useRef, useState } from "react";
import { Icon } from "../../components/Icon";
import { useActiveExtensions } from "../../app/extensions";
import { Modal } from "../../components/ui";
import { QueryResult } from "./api";
import { deleteStatements, editTarget, insertStatements, updateStatements, type EditTarget } from "./rowEdits";
import SqlCode from "./SqlCode";
import { colorizeJson } from "./monacoSetup";
import { resultCSV, resultJSON } from "./resultExport";
import { filteredIndexes, type ResultFilter, type FilterOperator } from "./resultFilter";
import { sortRowIndexes } from "./resultSort";

// Renders a query result set. Values are rendered as text; NULL is shown
// explicitly. Sorting and exports cover the currently received, bounded rows.
export interface ResultEditing {
  engine: string;
  /** Primary-key column names of a table, or null when it has none. */
  primaryKey: (schema: string, table: string) => string[] | null;
  /** Runs the statements through the editor, then reloads the result. */
  onApply: (statements: string[]) => void;
}

type CellEdits = Map<number, Map<number, string | null>>;

function cellText(value: unknown) {
  return value == null ? "" : typeof value === "object" ? JSON.stringify(value) : String(value);
}

export default function ResultsGrid({ result, editing, engine, onFilteredCount }: { result: QueryResult; editing?: ResultEditing; engine?: string; onFilteredCount?: (shown: number, total: number) => void }) {
  const [filters, setFilters] = useState<ResultFilter[]>([]);
  const [filterPanelOpen, setFilterPanelOpen] = useState(false);
  // The filter value input stays instantly responsive (it's just what's
  // shown in the box), but the actual scan - a full pass over every loaded
  // row, and whatever re-sort depends on its result - only runs 200ms after
  // typing stops, instead of once per keystroke.
  const [debouncedFilters, setDebouncedFilters] = useState(filters);
  useEffect(() => {
    const timer = window.setTimeout(() => setDebouncedFilters(filters), 200);
    return () => window.clearTimeout(timer);
  }, [filters]);
  const indexes = useMemo(() => filteredIndexes(result.rows, debouncedFilters, result.columnTypes), [result.rows, result.rowCount, result.columnTypes, debouncedFilters]);
  const visibleResult = useMemo(() => ({ ...result, rows: indexes.map(index => result.rows[index]), rowCount: indexes.length }), [result, indexes]);
  useEffect(() => { onFilteredCount?.(indexes.length, result.rows.length); }, [indexes.length, result.rows.length, onFilteredCount]);
  // Document engines return one JSON document per row; that reads better as
  // JSON than as a grid with a single stringified column, so it's the default
  // view for them. Every other engine still opens as Grid.
  const jsonByDefault = engine === "mongodb" || engine === "elasticsearch";
  const [view, setView] = useState<"grid" | "text" | "json">(jsonByDefault ? "json" : "grid");
  const target = useMemo(() => (editing ? editTarget(result.columnOrigins, editing.primaryKey) : null), [editing, result.columnOrigins]);
  const [editMode, setEditMode] = useState(false);
  const [edits, setEdits] = useState<CellEdits>(() => new Map());
  // Rows typed in at the end of the grid, and rows marked for deletion.
  const [drafts, setDrafts] = useState<Map<number, string | null>[]>([]);
  const [deletions, setDeletions] = useState<Set<number>>(() => new Set());
  const [reviewing, setReviewing] = useState(false);
  const changeCount = [...edits.values()].reduce((count, row) => count + row.size, 0) + drafts.length + deletions.size;
  const statements = useMemo(() => {
    if (!editing || !target) return [];
    const types = result.columnTypes;
    return [
      ...insertStatements(editing.engine, target, drafts, types),
      ...updateStatements(editing.engine, target, [...edits.entries()].map(([row, values]) => ({ row: result.rows[row], values })), types),
      ...deleteStatements(editing.engine, target, [...deletions].map((row) => result.rows[row]), types),
    ];
  }, [editing, target, edits, drafts, deletions, result.rows, result.columnTypes]);

  // A new result starts clean.
  useEffect(() => {
    setEdits(new Map());
    setDrafts([]);
    setDeletions(new Set());
    setEditMode(false);
  }, [result]);

  function setDraftCell(draft: number, column: number, value: string | null) {
    setDrafts((current) => current.map((row, index) => (index === draft ? new Map(row).set(column, value) : row)));
  }

  function toggleDeletion(row: number) {
    setDeletions((current) => {
      const next = new Set(current);
      if (!next.delete(row)) next.add(row);
      return next;
    });
  }

  function discard() {
    setEdits(new Map());
    setDrafts([]);
    setDeletions(new Set());
  }

  function setCell(row: number, column: number, value: string | null) {
    setEdits((current) => {
      const next = new Map(current);
      const values = new Map(next.get(row) ?? []);
      const original = result.rows[row]?.[column];
      if (value === null ? original == null : original != null && cellText(original) === value) values.delete(column);
      else values.set(column, value);
      if (values.size) next.set(row, values);
      else next.delete(row);
      return next;
    });
  }

  if (result.columns.length === 0) {
    return (
      <p className="p-3 text-[13px] text-emerald-700 dark:text-emerald-300">
        OK · {result.rowCount} row(s) affected · {result.durationMs}ms
      </p>
    );
  }

  return (
    <div className="flex h-full flex-col">
      <div className="flex min-h-9 flex-wrap items-center gap-2 border-b border-slate-200 bg-white px-2 py-1 text-xs text-slate-500 dark:border-slate-800 dark:bg-slate-950">
        <div className="flex items-center gap-0.5 rounded-md border border-slate-200 p-0.5 dark:border-slate-800">
          <ViewToggle active={view === "grid"} onClick={() => setView("grid")} icon="grid" label="Grid" />
          <ViewToggle active={view === "text"} onClick={() => setView("text")} icon="text" label="Text" />
          <ViewToggle active={view === "json"} onClick={() => setView("json")} icon="braces" label="JSON" />
        </div>
        <span className="h-4 w-px bg-slate-200 dark:bg-slate-800" />
        {editing && (
          <button
            type="button"
            disabled={!target}
            onClick={() => setEditMode((value) => !value)}
            title={target ? "Double-click a cell to change it" : "Editing needs a result from one table that includes its primary key"}
            className={`flex h-6 items-center gap-1 rounded border px-2 font-medium transition disabled:cursor-not-allowed disabled:opacity-40 ${editMode ? "border-amber-200 bg-amber-100 text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/15 dark:text-amber-300" : "border-slate-200 bg-white text-slate-600 hover:bg-slate-50 hover:text-slate-900 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800"}`}
          >
            <Icon name="pencil" size={12} />
            {editMode ? `Editing ${target?.table ?? ""}` : "Edit rows"}
          </button>
        )}
        {editMode && target && (
          <button type="button" onClick={() => setDrafts((current) => [...current, new Map()])} title="Type a new row at the end of the grid" className="flex h-6 items-center gap-1 rounded border border-slate-200 bg-white px-2 font-medium text-slate-600 hover:bg-slate-50 hover:text-slate-900 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800">
            <Icon name="plus" size={12} />
            Add row
          </button>
        )}
        {changeCount > 0 && (
          <>
            <button type="button" onClick={() => setReviewing(true)} className="flex h-6 items-center rounded bg-emerald-600 px-2 font-medium text-white hover:bg-emerald-500">
              Review {changeCount} change{changeCount === 1 ? "" : "s"}
            </button>
            <button type="button" onClick={discard} className="flex h-6 items-center rounded px-2 font-medium text-slate-500 hover:text-slate-800 dark:hover:text-slate-200">
              Discard
            </button>
          </>
        )}
        <button
          type="button"
          onClick={() => setFilterPanelOpen((open) => {
            const next = !open;
            if (next && filters.length === 0) setFilters([{ column: -1, operator: "contains", value: "" }]);
            return next;
          })}
          title="Filter the rows already loaded, without querying the database again"
          className={`flex h-6 items-center gap-1 rounded border px-2 font-medium transition ${filters.length ? "border-sky-200 bg-sky-100 text-sky-800 dark:border-sky-500/30 dark:bg-sky-500/15 dark:text-sky-300" : "border-slate-200 bg-white text-slate-600 hover:bg-slate-50 hover:text-slate-900 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800"}`}
        >
          <Icon name="filter" size={12} />
          Filter{filters.length ? ` (${filters.length})` : ""}
        </button>
        <div className="ml-auto flex items-center gap-1">
          <ExportButton result={visibleResult} kind="csv" />
          <ExportButton result={visibleResult} kind="json" />
        </div>
      </div>
      {filterPanelOpen && <div className="flex max-h-40 shrink-0 flex-col gap-1.5 overflow-auto border-b border-slate-200 px-2 py-1.5 text-xs dark:border-slate-800">
        <div className="flex justify-between text-[11px] text-slate-500">
          <span>On the rows already loaded here — no query is sent</span>
          {filters.length > 0 && (
            <button onClick={() => setFilters([])} className="font-medium text-rose-500 underline decoration-rose-300 decoration-1 underline-offset-2 hover:text-rose-600 hover:decoration-rose-400 dark:text-rose-400 dark:decoration-rose-700 dark:hover:text-rose-300">
              Clear filters
            </button>
          )}
        </div>
        {filters.map((filter, index) => {
          const update = (patch: Partial<ResultFilter>) => setFilters(current => current.map((item, i) => i === index ? { ...item, ...patch } : item));
          const field = "h-7 rounded border border-slate-200 bg-white px-2 dark:border-slate-700 dark:bg-slate-900";
          return <div key={index} className="flex items-center gap-2">
            <select aria-label={`Filter ${index + 1} column`} className={field} value={filter.column} onChange={e => update({ column: Number(e.target.value) })}><option value={-1}>Any column</option>{result.columns.map((column, i) => <option key={i} value={i}>{column}</option>)}</select>
            <select aria-label={`Filter ${index + 1} operator`} className={field} value={filter.operator} onChange={e => update({ operator: e.target.value as FilterOperator })}>{Object.entries({ contains: "Contains", eq: "Equals", neq: "Does not equal", gt: ">", gte: "≥", lt: "<", lte: "≤", null: "Is NULL", notNull: "Is not NULL" }).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select>
            {!["null", "notNull"].includes(filter.operator) && <input aria-label={`Filter ${index + 1} value`} className={`${field} min-w-0 flex-1`} value={filter.value} onChange={e => update({ value: e.target.value })} placeholder="Value" />}
            <button aria-label={`Remove filter ${index + 1}`} onClick={() => setFilters(current => current.filter((_, i) => i !== index))} className="text-slate-400 hover:text-slate-700 dark:hover:text-slate-200">×</button>
          </div>;
        })}
        {filters.length > 1 && (
          <div className="text-[11px] text-slate-400">All conditions must match</div>
        )}
        <button
          type="button"
          onClick={() => setFilters(current => [...current, { column: -1, operator: "contains", value: "" }])}
          className="flex h-6 w-fit items-center gap-1 self-start rounded border border-dashed border-slate-300 px-2 font-medium text-slate-500 hover:border-slate-400 hover:text-slate-700 dark:border-slate-700 dark:text-slate-400 dark:hover:border-slate-600 dark:hover:text-slate-200"
        >
          <Icon name="plus" size={11} />
          Add condition
        </button>
      </div>}
      <div className="min-h-0 flex-1 overflow-auto">
        {view === "grid" ? (
          <GridView result={result} indexes={indexes} filterKey={filters} editable={editMode ? target : null} edits={edits} onEdit={setCell} drafts={drafts} onDraftEdit={setDraftCell} deletions={deletions} onToggleDelete={toggleDeletion} />
        ) : view === "text" ? <TextView result={visibleResult} /> : <JsonView result={visibleResult} />}
      </div>
      {reviewing && editing && (
        <Modal title="Apply changes" onClose={() => setReviewing(false)} size="lg">
          <div className="space-y-3">
            <p className="text-[13px] text-slate-600 dark:text-slate-300">
              These statements run in the editor like any query, so policies apply and, in manual commit mode, nothing is saved until you press Commit. The result reloads afterwards.
            </p>
            <SqlCode sql={statements.map((statement) => statement + ";").join("\n")} className="max-h-72 rounded-md border border-slate-200 bg-slate-50 p-3 dark:border-slate-800 dark:bg-slate-900" />
            <div className="flex justify-end gap-2">
              <button type="button" onClick={() => setReviewing(false)} className="inline-flex h-8 items-center rounded-md border border-slate-200 px-3 text-[13px] text-slate-600 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800">Cancel</button>
              <button type="button" disabled={!statements.length} onClick={() => { editing.onApply(statements); setReviewing(false); discard(); setEditMode(false); }} className="inline-flex h-8 items-center rounded-md bg-emerald-600 px-3 text-[13px] font-medium text-white hover:bg-emerald-500 disabled:opacity-50">
                Apply {statements.length} statement{statements.length === 1 ? "" : "s"}
              </button>
            </div>
          </div>
        </Modal>
      )}
    </div>
  );
}

function GridView({ result, indexes, filterKey, editable, edits, onEdit, drafts = [], onDraftEdit, deletions, onToggleDelete }: { result: QueryResult; indexes: number[]; filterKey: ResultFilter[]; editable?: EditTarget | null; edits?: CellEdits; onEdit?: (row: number, column: number, value: string | null) => void; drafts?: Map<number, string | null>[]; onDraftEdit?: (draft: number, column: number, value: string | null) => void; deletions?: Set<number>; onToggleDelete?: (row: number) => void }) {
  const [editingCell, setEditingCell] = useState<{ row: number; column: number; text: string } | null>(null);
  const Mark = useActiveExtensions().find((item) => item.columnMark)?.columnMark;
  const [cellView, setCellView] = useState<{ column: string; value: unknown } | null>(null);
  const [copyState, setCopyState] = useState("");
  const [widths, setWidths] = useState<Record<number, number>>({});
  const [sort, setSort] = useState<{ col: number; dir: "asc" | "desc" } | null>(null);
  const scrollerRef = useRef<HTMLDivElement | null>(null);
  const [scrollTop, setScrollTop] = useState(0);
  const [viewportHeight, setViewportHeight] = useState(480);
  const rowHeight = 32;
  const overscan = 16;

  function toggleSort(col: number) {
    setSort((s) => (!s || s.col !== col ? { col, dir: "asc" } : s.dir === "asc" ? { col, dir: "desc" } : null));
  }

  // Row order as indexes into result.rows, so edits stay tied to their row.
  const order = useMemo(() => {
    if (!sort) return indexes;
    const { col, dir } = sort;
    return sortRowIndexes(result.rows, indexes, col, result.columnTypes?.[col], dir);
    // rowCount grows while a result streams in; the array itself is reused.
  }, [result.rows, result.rowCount, result.columnTypes, sort, indexes]);

  useEffect(() => {
    const el = scrollerRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver((entries) => {
      const height = Math.floor(entries[0]?.contentRect.height ?? 0);
      setViewportHeight(height);
    });
    observer.observe(el);
    setViewportHeight(Math.floor(el.clientHeight));
    return () => observer.disconnect();
  }, []);

  useEffect(() => { scrollerRef.current?.scrollTo({ top: 0 }); setScrollTop(0); }, [filterKey]);
  const totalRows = order.length;
  const start = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
  const visibleCount = Math.max(1, Math.ceil(viewportHeight / rowHeight) + overscan * 2);
  const end = Math.min(totalRows, start + visibleCount);
  const visibleRows = order.slice(start, end);
  const paddingTop = start * rowHeight;
  const paddingBottom = Math.max(0, (totalRows - end) * rowHeight);

  return (
    <>
    <div ref={scrollerRef} className="h-full overflow-auto" onScroll={(e) => setScrollTop((e.currentTarget as HTMLDivElement).scrollTop)}>
      <table className="min-w-full text-left text-xs">
        <thead className="sticky top-0 z-10 bg-[#f3f4f6] text-slate-500 dark:bg-slate-900 dark:text-slate-400">
          <tr>
            <th className="w-12 border-b border-r border-slate-200 px-2 py-1.5 font-medium dark:border-slate-800">#</th>
            {result.columns.map((c, ci) => {
              const active = sort?.col === ci;
              return (
                <th
                  key={`${ci}:${c}`}
                  onClick={() => toggleSort(ci)}
                  className="cursor-pointer select-none whitespace-nowrap border-b border-r border-slate-200 px-2 py-1.5 font-medium hover:bg-slate-200/50 dark:border-slate-800 dark:hover:bg-slate-800/50"
                  title="Sort by this column"
                  style={{ minWidth: widths[ci] ?? 120, width: widths[ci] }}
                >
                  <div className="flex items-center gap-1.5">
                    {Mark && <Mark result={result} column={c} />}
                    <span className="text-slate-700 dark:text-slate-200">
                      {c}
                    </span>
                    {result.columnTypes?.[ci] && <span className="text-[10px] font-normal text-slate-400">{result.columnTypes[ci]}</span>}
                    <Icon
                      name={active ? "chevron-down" : "sort"}
                      size={11}
                      className={`transition ${active ? "text-brand-500" : "text-slate-300 dark:text-slate-600"} ${active && sort?.dir === "asc" ? "rotate-180" : ""}`}
                    />
                    <span role="separator" aria-label={`Resize ${c}`} className="-mr-2 ml-auto h-5 w-2 shrink-0 cursor-col-resize border-r-2 border-transparent hover:border-slate-400 dark:hover:border-slate-500" onClick={event => event.stopPropagation()} onPointerDown={event => {
                      event.preventDefault(); event.stopPropagation();
                      const start = event.clientX, width = event.currentTarget.closest("th")?.getBoundingClientRect().width ?? 120;
                      const move = (ev: PointerEvent) => setWidths(current => ({ ...current, [ci]: Math.max(70, Math.min(1200, width + ev.clientX - start)) }));
                      const up = () => { window.removeEventListener("pointermove", move); window.removeEventListener("pointerup", up); window.removeEventListener("pointercancel", up); };
                      window.addEventListener("pointermove", move); window.addEventListener("pointerup", up); window.addEventListener("pointercancel", up);
                    }} />
                  </div>
                </th>
              );
            })}
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {paddingTop > 0 && (
            <tr aria-hidden="true">
              <td colSpan={result.columns.length + 1} style={{ height: `${paddingTop}px`, border: 0, padding: 0 }} />
            </tr>
          )}
          {visibleRows.map((rowIndex, i) => {
            const row = result.rows[rowIndex];
            const rowEdits = edits?.get(rowIndex);
            return (
              <tr key={rowIndex} style={{ height: rowHeight }} className={`hover:bg-slate-50 dark:hover:bg-slate-900 ${deletions?.has(rowIndex) ? "bg-rose-50 line-through decoration-rose-400 dark:bg-rose-500/10" : ""}`}>
                <td className="whitespace-nowrap border-r border-slate-100 px-2 py-1.5 text-slate-400 dark:border-slate-800 dark:text-slate-500">
                  {editable && onToggleDelete ? (
                    <button
                      type="button"
                      onClick={() => onToggleDelete(rowIndex)}
                      title={deletions?.has(rowIndex) ? "Keep this row" : "Mark this row for deletion"}
                      className={`w-full text-left no-underline ${deletions?.has(rowIndex) ? "text-rose-600 dark:text-rose-400" : "hover:text-rose-600 dark:hover:text-rose-400"}`}
                    >
                      {deletions?.has(rowIndex) ? "✕" : start + i + 1}
                    </button>
                  ) : start + i + 1}
                </td>
                {row.map((cell, j) => {
                  const canEdit = Boolean(editable && editable.columns[j] && !editable.key.includes(j));
                  const changed = Boolean(rowEdits?.has(j));
                  const shown = changed ? rowEdits!.get(j) : cell;
                  const inEdit = editingCell?.row === rowIndex && editingCell.column === j;
                  const commit = (value: string | null) => { onEdit?.(rowIndex, j, value); setEditingCell(null); };
                  return (
                    <td
                      key={j}
                      title={canEdit ? "Double-click to edit" : "Double-click to inspect or copy"}
                      onDoubleClick={() => {
                        if (canEdit) { setEditingCell({ row: rowIndex, column: j, text: cellText(shown) }); return; }
                        setCopyState(""); setCellView({ column: result.columns[j], value: cell });
                      }}
                      style={{ maxWidth: widths[j] ?? 480 }}
                      className={`overflow-hidden text-ellipsis whitespace-nowrap border-r border-slate-100 px-2 py-1.5 text-slate-700 dark:border-slate-800 dark:text-slate-200 ${changed ? "bg-amber-50 dark:bg-amber-500/10" : ""}`}
                    >
                      {inEdit ? (
                        <span className="flex items-center gap-1">
                          <input
                            autoFocus
                            value={editingCell.text}
                            onChange={(event) => setEditingCell({ row: rowIndex, column: j, text: event.target.value })}
                            onKeyDown={(event) => { if (event.key === "Enter") commit(editingCell.text); if (event.key === "Escape") setEditingCell(null); }}
                            onBlur={() => commit(editingCell.text)}
                            className="h-6 w-full min-w-[80px] rounded border border-sky-400 bg-white px-1 font-mono text-[12px] outline-none dark:bg-slate-900"
                          />
                          <button type="button" title="Set NULL" onMouseDown={(event) => { event.preventDefault(); commit(null); }} className="rounded border border-slate-300 px-1 text-[10px] text-slate-500 dark:border-slate-600">NULL</button>
                        </span>
                      ) : renderCell(shown)}
                    </td>
                  );
                })}
              </tr>
            );
          })}
          {paddingBottom > 0 && (
            <tr aria-hidden="true">
              <td colSpan={result.columns.length + 1} style={{ height: `${paddingBottom}px`, border: 0, padding: 0 }} />
            </tr>
          )}
          {drafts.map((draft, draftIndex) => {
            const key = -1 - draftIndex;
            return (
              <tr key={key} style={{ height: rowHeight }} className="bg-emerald-50/60 dark:bg-emerald-500/10">
                <td className="whitespace-nowrap border-r border-slate-100 px-2 py-1.5 text-[10px] font-medium uppercase text-emerald-700 dark:border-slate-800 dark:text-emerald-300">new</td>
                {result.columns.map((_, j) => {
                  const canEdit = Boolean(editable && editable.columns[j]);
                  const value = draft.get(j);
                  const inEdit = editingCell?.row === key && editingCell.column === j;
                  const commit = (text: string | null) => { onDraftEdit?.(draftIndex, j, text); setEditingCell(null); };
                  return (
                    <td
                      key={j}
                      title={canEdit ? "Double-click to type a value; columns left empty keep their default" : "This column cannot be written"}
                      onDoubleClick={() => canEdit && setEditingCell({ row: key, column: j, text: value ?? "" })}
                      style={{ maxWidth: widths[j] ?? 480 }}
                      className="overflow-hidden text-ellipsis whitespace-nowrap border-r border-slate-100 px-2 py-1.5 text-slate-700 dark:border-slate-800 dark:text-slate-200"
                    >
                      {inEdit ? (
                        <span className="flex items-center gap-1">
                          <input
                            autoFocus
                            value={editingCell.text}
                            onChange={(event) => setEditingCell({ row: key, column: j, text: event.target.value })}
                            onKeyDown={(event) => { if (event.key === "Enter") commit(editingCell.text); if (event.key === "Escape") setEditingCell(null); }}
                            onBlur={() => commit(editingCell.text)}
                            className="h-6 w-full min-w-[80px] rounded border border-sky-400 bg-white px-1 font-mono text-[12px] outline-none dark:bg-slate-900"
                          />
                          <button type="button" title="Set NULL" onMouseDown={(event) => { event.preventDefault(); commit(null); }} className="rounded border border-slate-300 px-1 text-[10px] text-slate-500 dark:border-slate-600">NULL</button>
                        </span>
                      ) : value === null ? <span className="text-slate-400">NULL</span> : value ? value : <span className="text-slate-300 dark:text-slate-600">default</span>}
                    </td>
                  );
                })}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
    {cellView && <div className="fixed inset-0 z-50 grid place-items-center bg-black/40 p-6" role="presentation" onClick={() => setCellView(null)}>
      <div role="dialog" aria-modal="true" aria-label={`Cell ${cellView.column}`} className="w-full max-w-3xl space-y-3 rounded-lg bg-white p-5 dark:bg-slate-900" onClick={e => e.stopPropagation()}>
        <div className="flex justify-between"><strong>{cellView.column}</strong><button onClick={() => setCellView(null)}>Close</button></div>
        <textarea readOnly className="h-80 w-full rounded border bg-transparent p-2 font-mono text-xs" value={cellView.value == null ? "NULL" : typeof cellView.value === "object" ? JSON.stringify(cellView.value, null, 2) : String(cellView.value)} />
        <button className="rounded border px-3 py-1" onClick={() => { const value = cellView.value == null ? "NULL" : typeof cellView.value === "object" ? JSON.stringify(cellView.value, null, 2) : String(cellView.value); void navigator.clipboard.writeText(value).then(() => setCopyState("Copied"), () => setCopyState("Clipboard unavailable; select and copy the text above.")); }}>Copy value</button>
        <span role="status" className="ml-3 text-xs">{copyState}</span>
      </div>
    </div>}
    </>
  );
}

function TextView({ result }: { result: QueryResult }) {
  const header = result.columns.join("\t");
  const body = result.rows.map((r) => r.map((c) => (c === null || c === undefined ? "NULL" : String(c))).join("\t")).join("\n");
  return (
    <pre className="whitespace-pre p-3 font-mono text-xs leading-5 text-slate-700 dark:text-slate-200">{header + "\n" + body}</pre>
  );
}

// A single-column result (Mongo, Elasticsearch and Redis all shape their
// rows this way) is one JSON value per row already — parse it back out
// instead of nesting it in a {"document": "...(escaped)..."} object. A
// multi-column result becomes one object per row, keyed by column name.
function resultToJsonRows(result: QueryResult): unknown[] {
  return result.rows.map((row) => {
    if (result.columns.length === 1) {
      const cell = row[0];
      // Already an object/array cell (Mongo, Elasticsearch): unwrap as-is.
      if (cell !== null && typeof cell === "object") return cell;
      // A JSON-text cell (e.g. a jsonb column) that parses to an
      // object/array: unwrap the same way. A plain string or number keeps
      // its column name below, same as any other single-column result.
      if (typeof cell === "string") {
        try {
          const parsed: unknown = JSON.parse(cell);
          if (parsed !== null && typeof parsed === "object") return parsed;
        } catch { /* not JSON text; fall through to the named-column form */ }
      }
    }
    const doc: Record<string, unknown> = {};
    result.columns.forEach((column, i) => { doc[column] = row[i]; });
    return doc;
  });
}

function JsonView({ result }: { result: QueryResult }) {
  const [html, setHtml] = useState("");
  const json = useMemo(() => JSON.stringify(resultToJsonRows(result), null, 2), [result]);
  useEffect(() => {
    let alive = true;
    colorizeJson(json).then((result) => { if (alive) setHtml(result); }, () => undefined);
    return () => { alive = false; };
  }, [json]);
  const classes = "whitespace-pre p-3 font-mono text-xs leading-5 text-slate-700 dark:text-slate-200";
  return html ? <pre className={classes} dangerouslySetInnerHTML={{ __html: html }} /> : <pre className={classes}>{json}</pre>;
}

function ViewToggle({ active, onClick, icon, label }: { active: boolean; onClick: () => void; icon: "grid" | "text" | "braces"; label: string }) {
  return (
    <button
      onClick={onClick}
      className={`flex h-6 items-center gap-1.5 rounded px-2 font-medium transition ${
        active ? "bg-slate-100 text-slate-800 dark:bg-slate-800 dark:text-slate-100" : "text-slate-500 hover:text-slate-800 dark:hover:text-slate-200"
      }`}
    >
      <Icon name={icon} size={13} />
      {label}
    </button>
  );
}

function ExportButton({ result, kind }: { result: QueryResult; kind: "csv" | "json" }) {
  return (
    <button
      onClick={() => downloadResult(result, kind)}
      className="flex h-6 items-center gap-1 rounded border border-slate-200 bg-white px-2 font-medium text-slate-600 transition hover:bg-slate-50 hover:text-slate-900 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800"
      title={`Export results as ${kind.toUpperCase()}`}
    >
      <Icon name="save" size={12} />
      {kind.toUpperCase()}
    </button>
  );
}

function downloadResult(result: QueryResult, kind: "csv" | "json") {
  let content: string;
  let mime: string;
  if (kind === "json") {
    content = resultJSON(result);
    mime = "application/json";
  } else {
    content = resultCSV(result);
    mime = "text/csv";
  }
  const blob = new Blob([content], { type: `${mime};charset=utf-8` });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `rowset-results-${Date.now()}.${kind}`;
  a.click();
  URL.revokeObjectURL(url);
}

function renderCell(v: unknown) {
  if (v === null || v === undefined) return <span className="text-slate-400">NULL</span>;
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}
