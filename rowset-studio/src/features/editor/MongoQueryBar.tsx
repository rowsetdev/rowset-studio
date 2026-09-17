import { useEffect, useState } from "react";
import { Icon } from "../../components/Icon";
import {
  isMongoAggregateQuery,
  mongoAggregatePartsToShell,
  mongoAggregateQuery,
  mongoAggregateSourceToParts,
  mongoPartsToShell,
  mongoQuery,
  mongoSourceToParts,
  type MongoAggregateParts,
  type MongoQueryParts,
} from "./mongoQuery";

const EMPTY: MongoQueryParts = { collection: "", filter: "{}", project: "{}", sort: "{}", skip: "0", limit: "100", maxTimeMs: "0" };
const EMPTY_AGGREGATE: MongoAggregateParts = { collection: "", pipeline: "[]" };

const modeTab = (active: boolean) => `h-7 rounded-md px-2.5 text-[12px] font-medium transition ${active ? "bg-orange-600 text-white" : "bg-white text-slate-600 hover:bg-slate-100 dark:bg-slate-950 dark:text-slate-300 dark:hover:bg-slate-800"}`;

// A Compass-style bar for find() and a plain pipeline editor for
// aggregate(), kept in sync with the raw db.collection.find()/aggregate()
// editor below it: typing here rewrites the editor text, and switching tabs
// or mode re-reads the editor text into these fields.
export default function MongoQueryBar({ tabKey, sql, onChange, onRun }: { tabKey: string; sql: string; onChange: (sql: string) => void; onRun: () => void }) {
  const [parts, setParts] = useState<MongoQueryParts>(EMPTY);
  const [aggregateParts, setAggregateParts] = useState<MongoAggregateParts>(EMPTY_AGGREGATE);
  const [aggregate, setAggregate] = useState(false);
  const [expanded, setExpanded] = useState(false);

  useEffect(() => {
    const isAggregate = isMongoAggregateQuery(sql);
    setAggregate(isAggregate);
    if (isAggregate) {
      try { setAggregateParts(mongoAggregateSourceToParts(sql)); }
      catch { setAggregateParts((current) => ({ ...current, collection: current.collection || "collection" })); }
      return;
    }
    try {
      setParts(mongoSourceToParts(sql));
    } catch {
      setParts((current) => ({ ...current, collection: current.collection || "collection" }));
    }
    // Only re-read the editor when switching tabs, so typing in the editor
    // itself does not fight the bar's own edits.
  }, [tabKey]);

  const commit = (next: MongoQueryParts) => {
    setParts(next);
    onChange(mongoPartsToShell(next));
  };
  const commitAggregate = (next: MongoAggregateParts) => {
    setAggregateParts(next);
    onChange(mongoAggregatePartsToShell(next));
  };
  const switchMode = (next: boolean) => {
    setAggregate(next);
    if (next) { setAggregateParts((current) => ({ ...current, collection: current.collection || parts.collection })); onChange(mongoAggregateQuery(aggregateParts.collection || parts.collection)); }
    else { setParts((current) => ({ ...current, collection: current.collection || aggregateParts.collection })); onChange(mongoQuery(parts.collection || aggregateParts.collection)); }
  };

  const hasOptions = parts.project.trim() !== "{}" || parts.skip.trim() !== "0" || parts.maxTimeMs.trim() !== "0";

  return (
    <div className="border-b border-slate-200 bg-slate-50 px-3 py-2 dark:border-slate-800 dark:bg-slate-900/40">
      <div className="flex items-center gap-2">
        <div className="flex shrink-0 gap-1 rounded-md border border-slate-300 bg-white p-0.5 dark:border-slate-700 dark:bg-slate-950">
          <button type="button" className={modeTab(!aggregate)} onClick={() => switchMode(false)}>Find</button>
          <button type="button" className={modeTab(aggregate)} onClick={() => switchMode(true)}>Aggregate</button>
        </div>
        {aggregate ? (
          <input
            value={aggregateParts.collection}
            onChange={(event) => commitAggregate({ ...aggregateParts, collection: event.target.value })}
            onKeyDown={(event) => { if (event.key === "Enter") onRun(); }}
            placeholder="collection"
            spellCheck={false}
            className="h-8 w-40 shrink-0 rounded-md border border-slate-300 bg-white px-2.5 font-mono text-[12px] text-slate-800 outline-none focus:border-orange-400 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
          />
        ) : (
          <input
            value={parts.filter}
            onChange={(event) => commit({ ...parts, filter: event.target.value })}
            onKeyDown={(event) => { if (event.key === "Enter") onRun(); }}
            placeholder="{ field: value }"
            spellCheck={false}
            className="h-8 flex-1 rounded-md border border-slate-300 bg-white px-2.5 font-mono text-[12px] text-slate-800 outline-none focus:border-orange-400 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
          />
        )}
        {!aggregate && (
          <button
            type="button"
            onClick={() => setExpanded((v) => !v)}
            className={`flex h-8 shrink-0 items-center gap-1 rounded-md border px-2.5 text-[12px] ${hasOptions ? "border-orange-300 text-orange-700 dark:border-orange-800 dark:text-orange-400" : "border-slate-300 text-slate-600 dark:border-slate-700 dark:text-slate-300"}`}
          >
            Options
            <Icon name={expanded ? "chevron-down" : "chevron-right"} className="h-3 w-3" />
          </button>
        )}
        <button type="button" onClick={onRun} className="h-8 shrink-0 rounded-md bg-orange-600 px-3 text-[12px] font-medium text-white hover:bg-orange-700">
          {aggregate ? "Run pipeline" : "Find"}
        </button>
      </div>
      {aggregate && (
        <textarea
          value={aggregateParts.pipeline}
          onChange={(event) => commitAggregate({ ...aggregateParts, pipeline: event.target.value })}
          placeholder={'[\n  { "$match": {} }\n]'}
          spellCheck={false}
          rows={4}
          className="mt-2 w-full resize-y rounded-md border border-slate-300 bg-white p-2 font-mono text-[12px] text-slate-800 outline-none focus:border-orange-400 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
        />
      )}
      {!aggregate && expanded && (
        <div className="mt-2 grid grid-cols-5 gap-2">
          <Field label="Project" value={parts.project} mono onCommit={(v) => commit({ ...parts, project: v })} placeholder="{ field: 1 }" />
          <Field label="Sort" value={parts.sort} mono onCommit={(v) => commit({ ...parts, sort: v })} placeholder="{ field: 1 }" />
          <Field label="Skip" value={parts.skip} onCommit={(v) => commit({ ...parts, skip: v })} placeholder="0" />
          <Field label="Limit" value={parts.limit} onCommit={(v) => commit({ ...parts, limit: v })} placeholder="100" />
          <Field label="Max Time MS" value={parts.maxTimeMs} onCommit={(v) => commit({ ...parts, maxTimeMs: v })} placeholder="0" />
        </div>
      )}
    </div>
  );
}

function Field({ label, value, onCommit, placeholder, mono }: { label: string; value: string; onCommit: (v: string) => void; placeholder: string; mono?: boolean }) {
  return (
    <label className="flex flex-col gap-0.5">
      <span className="text-[11px] text-slate-500 dark:text-slate-400">{label}</span>
      <input
        defaultValue={value}
        key={value}
        onBlur={(event) => onCommit(event.target.value)}
        onKeyDown={(event) => { if (event.key === "Enter") event.currentTarget.blur(); }}
        placeholder={placeholder}
        spellCheck={false}
        className={`h-7 rounded-md border border-slate-300 bg-white px-2 text-[12px] text-slate-800 outline-none focus:border-orange-400 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100 ${mono ? "font-mono" : ""}`}
      />
    </label>
  );
}
