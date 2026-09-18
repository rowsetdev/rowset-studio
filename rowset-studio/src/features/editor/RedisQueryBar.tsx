import { useEffect, useRef, useState } from "react";

export function redisQuery() {
  return `{"pattern":"*","type":"","limit":100}`;
}

interface RedisParts { pattern: string; type: string; limit: string }

function toParts(sql: string): RedisParts {
  const parsed = JSON.parse(sql);
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("not an object");
  return { pattern: typeof parsed.pattern === "string" ? parsed.pattern : "*", type: typeof parsed.type === "string" ? parsed.type : "", limit: parsed.limit != null ? String(parsed.limit) : "100" };
}

function toSql(parts: RedisParts): string {
  const fields = [`"pattern":${JSON.stringify(parts.pattern.trim() || "*")}`];
  if (parts.type.trim()) fields.push(`"type":${JSON.stringify(parts.type.trim())}`);
  fields.push(`"limit":${Number(parts.limit) > 0 ? Number(parts.limit) : 100}`);
  return `{${fields.join(",")}}`;
}

const TYPES = ["", "string", "hash", "list", "set", "zset", "stream"];

// A Compass-bar-style row for Redis SCAN: pattern, type filter and a limit,
// kept in sync with the raw {pattern,type,limit} JSON below it.
export default function RedisQueryBar({ tabKey, sql, onChange, onRun }: { tabKey: string; sql: string; onChange: (sql: string) => void; onRun: () => void }) {
  const [parts, setParts] = useState<RedisParts>({ pattern: "*", type: "", limit: "100" });

  // The text this bar last wrote; any other change (tab switch, typing in
  // the editor, a history pick) is re-read into the fields.
  const written = useRef<{ tab: string; sql: string } | null>(null);
  useEffect(() => {
    if (written.current?.tab === tabKey && written.current.sql === sql) return;
    try { setParts(toParts(sql)); } catch { /* keep last valid parts while the JSON is mid-edit */ }
  }, [tabKey, sql]);

  const commit = (next: RedisParts) => { setParts(next); const text = toSql(next); written.current = { tab: tabKey, sql: text }; onChange(text); };

  return (
    <div className="flex items-center gap-2 border-b border-slate-200 bg-slate-50 px-3 py-2 dark:border-slate-800 dark:bg-slate-900/40">
      <span className="shrink-0 text-[12px] font-medium text-slate-500 dark:text-slate-400">Pattern</span>
      <input
        value={parts.pattern}
        onChange={(event) => commit({ ...parts, pattern: event.target.value })}
        onKeyDown={(event) => { if (event.key === "Enter") onRun(); }}
        placeholder="*"
        spellCheck={false}
        className="h-8 flex-1 rounded-md border border-slate-300 bg-white px-2.5 font-mono text-[12px] text-slate-800 outline-none focus:border-orange-400 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
      />
      <span className="shrink-0 text-[12px] font-medium text-slate-500 dark:text-slate-400">Type</span>
      <select
        value={parts.type}
        onChange={(event) => commit({ ...parts, type: event.target.value })}
        className="h-8 shrink-0 rounded-md border border-slate-300 bg-white px-2 text-[12px] text-slate-700 outline-none focus:border-orange-400 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-200"
      >
        {TYPES.map((t) => <option key={t} value={t}>{t || "Any"}</option>)}
      </select>
      <span className="shrink-0 text-[12px] font-medium text-slate-500 dark:text-slate-400">Limit</span>
      <input
        value={parts.limit}
        onChange={(event) => commit({ ...parts, limit: event.target.value })}
        onKeyDown={(event) => { if (event.key === "Enter") onRun(); }}
        placeholder="100"
        className="h-8 w-20 shrink-0 rounded-md border border-slate-300 bg-white px-2 text-[12px] text-slate-800 outline-none focus:border-orange-400 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
      />
      <button type="button" onClick={onRun} className="h-8 shrink-0 rounded-md bg-orange-600 px-3 text-[12px] font-medium text-white hover:bg-orange-700">
        Scan
      </button>
    </div>
  );
}
