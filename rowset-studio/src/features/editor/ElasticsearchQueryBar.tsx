import { useEffect, useState } from "react";

export function elasticsearchQuery() {
  return `{"index":"","query":{"match_all":{}},"size":100}`;
}

interface EsParts {
  index: string;
  query: string;
  size: string;
  // sort/searchAfter/aggs are edited only in the raw JSON below the bar;
  // this keeps them intact when the bar's own fields are changed.
  rest: Record<string, unknown>;
}

function toParts(sql: string): EsParts {
  const parsed = JSON.parse(sql);
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("not an object");
  const { index, query, size, ...rest } = parsed;
  return {
    index: typeof index === "string" ? index : "",
    query: query != null ? JSON.stringify(query) : "{}",
    size: size != null ? String(size) : "100",
    rest,
  };
}

function toSql(parts: EsParts): string {
  let query: unknown;
  try { query = JSON.parse(parts.query.trim() || "{}"); } catch { query = undefined; }
  // Elasticsearch rejects an empty query clause ({}), so treat it as "match all".
  if (query !== undefined && typeof query === "object" && query !== null && !Array.isArray(query) && Object.keys(query).length === 0) {
    query = { match_all: {} };
  }
  const fields = [`"index":${JSON.stringify(parts.index.trim())}`];
  fields.push(`"query":${query !== undefined ? JSON.stringify(query) : parts.query}`);
  fields.push(`"size":${Number(parts.size) > 0 ? Number(parts.size) : 100}`);
  for (const [key, value] of Object.entries(parts.rest)) fields.push(`${JSON.stringify(key)}:${JSON.stringify(value)}`);
  return `{${fields.join(",")}}`;
}

// A bar for Elasticsearch _search: index, a JSON query body and a size,
// kept in sync with the raw {index,query,size} JSON below it.
export default function ElasticsearchQueryBar({ tabKey, sql, onChange, onRun }: { tabKey: string; sql: string; onChange: (sql: string) => void; onRun: () => void }) {
  const [parts, setParts] = useState<EsParts>({ index: "", query: '{"match_all":{}}', size: "100", rest: {} });

  useEffect(() => {
    try { setParts(toParts(sql)); } catch { /* keep last valid parts while the JSON is mid-edit */ }
  }, [tabKey]);

  const commit = (next: EsParts) => { setParts(next); onChange(toSql(next)); };

  return (
    <div className="flex items-center gap-2 border-b border-slate-200 bg-slate-50 px-3 py-2 dark:border-slate-800 dark:bg-slate-900/40">
      <span className="shrink-0 text-[12px] font-medium text-slate-500 dark:text-slate-400">Index</span>
      <input
        value={parts.index}
        onChange={(event) => commit({ ...parts, index: event.target.value })}
        onKeyDown={(event) => { if (event.key === "Enter") onRun(); }}
        placeholder="my-index"
        spellCheck={false}
        className="h-8 w-40 shrink-0 rounded-md border border-slate-300 bg-white px-2.5 font-mono text-[12px] text-slate-800 outline-none focus:border-orange-400 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
      />
      <span className="shrink-0 text-[12px] font-medium text-slate-500 dark:text-slate-400">Query</span>
      <input
        value={parts.query}
        onChange={(event) => commit({ ...parts, query: event.target.value })}
        onKeyDown={(event) => { if (event.key === "Enter") onRun(); }}
        placeholder='{"match_all":{}}'
        spellCheck={false}
        className="h-8 flex-1 rounded-md border border-slate-300 bg-white px-2.5 font-mono text-[12px] text-slate-800 outline-none focus:border-orange-400 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
      />
      <span className="shrink-0 text-[12px] font-medium text-slate-500 dark:text-slate-400">Size</span>
      <input
        value={parts.size}
        onChange={(event) => commit({ ...parts, size: event.target.value })}
        onKeyDown={(event) => { if (event.key === "Enter") onRun(); }}
        placeholder="100"
        className="h-8 w-20 shrink-0 rounded-md border border-slate-300 bg-white px-2 text-[12px] text-slate-800 outline-none focus:border-orange-400 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
      />
      <button type="button" onClick={onRun} className="h-8 shrink-0 rounded-md bg-orange-600 px-3 text-[12px] font-medium text-white hover:bg-orange-700">
        Search
      </button>
    </div>
  );
}
