import { useState } from "react";
import { mongoInsert, mongoUpdate, mongoDelete, mongoTxnInsert, mongoTxnUpdate, mongoTxnDelete, redisWrite, redisDelete, elasticsearchIndex, elasticsearchUpdate, elasticsearchDelete } from "./api";
import { ApiError } from "../../lib/api";

type Engine = "mongodb" | "redis" | "valkey" | "elasticsearch";
type Mode = "insert" | "update" | "delete";

const inputClass = "h-8 w-full rounded-md border border-slate-300 bg-white px-2.5 font-mono text-[12px] text-slate-800 outline-none focus:border-brand-400 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100";
const areaClass = "w-full flex-1 min-h-[140px] rounded-md border border-slate-300 bg-white p-2.5 font-mono text-[12px] text-slate-800 outline-none focus:border-brand-400 dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100";
const tabClass = (active: boolean) => `h-7 rounded-md px-3 text-[12px] font-medium transition ${active ? "bg-brand-600 text-white" : "bg-slate-100 text-slate-600 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700"}`;

function parseJSON(label: string, text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    throw new Error(`${label} must be valid JSON`);
  }
}

// A write dialog for engines with no SQL: MongoDB documents, Redis/Valkey
// keys and Elasticsearch documents each get their own minimal form, backed
// by the write endpoints that share the SQL guardrails (policy, read-only,
// audit) with the grid's UPDATE/DELETE statements.
export default function NoSqlWriteDialog({ connectionId, engine, database, txnId, onClose, onWritten }: { connectionId: string; engine: Engine; database: string; /** Open MongoDB transaction: writes join it instead of running standalone. */ txnId?: string; onClose: () => void; onWritten: () => void }) {
  const [mode, setMode] = useState<Mode>("insert");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState("");

  const [collection, setCollection] = useState("");
  const [document, setDocument] = useState('{\n  \n}');
  const [filter, setFilter] = useState('{\n  \n}');
  const [update, setUpdate] = useState('{\n  "$set": {}\n}');

  const [index, setIndex] = useState("");
  const [id, setId] = useState("");

  const [key, setKey] = useState("");
  const [keyType, setKeyType] = useState<"string" | "hash">("string");
  const [field, setField] = useState("");
  const [value, setValue] = useState("");
  const [ttl, setTtl] = useState("");

  async function run() {
    setBusy(true);
    setError("");
    setResult("");
    try {
      if (engine === "mongodb") {
        if (!collection.trim()) throw new Error("Collection is required");
        if (mode === "insert") {
          const response = txnId
            ? await mongoTxnInsert(connectionId, txnId, { collection, document: parseJSON("Document", document) })
            : await mongoInsert(connectionId, { database, collection, document: parseJSON("Document", document) });
          setResult(`Inserted _id: ${JSON.stringify(response.id)}`);
        } else if (mode === "update") {
          const response = txnId
            ? await mongoTxnUpdate(connectionId, txnId, { collection, filter: parseJSON("Filter", filter), update: parseJSON("Update", update) })
            : await mongoUpdate(connectionId, { database, collection, filter: parseJSON("Filter", filter), update: parseJSON("Update", update) });
          setResult(`Matched ${response.matchedCount}, modified ${response.modifiedCount}`);
        } else {
          const response = txnId
            ? await mongoTxnDelete(connectionId, txnId, { collection, filter: parseJSON("Filter", filter) })
            : await mongoDelete(connectionId, { database, collection, filter: parseJSON("Filter", filter) });
          setResult(`Deleted ${response.deletedCount}`);
        }
      } else if (engine === "elasticsearch") {
        if (!index.trim() || !id.trim()) throw new Error("Index and id are required");
        if (mode === "insert") {
          const response = await elasticsearchIndex(connectionId, { index, id, document: parseJSON("Document", document) });
          setResult(`Indexed _id: ${response.id}`);
        } else if (mode === "update") {
          await elasticsearchUpdate(connectionId, { index, id, doc: parseJSON("Doc", update) });
          setResult("Updated.");
        } else {
          await elasticsearchDelete(connectionId, { index, id });
          setResult("Deleted.");
        }
      } else {
        if (!key.trim()) throw new Error("Key is required");
        if (mode === "delete") {
          const response = await redisDelete(connectionId, { database, key });
          setResult(`Deleted ${response.deletedCount}`);
        } else {
          if (keyType === "hash" && !field.trim()) throw new Error("Field is required for a hash write");
          const ttlSeconds = ttl.trim() ? Number(ttl) : undefined;
          if (ttlSeconds !== undefined && (!Number.isFinite(ttlSeconds) || ttlSeconds < 0)) throw new Error("TTL must be a positive number of seconds");
          await redisWrite(connectionId, { database, key, type: keyType, field: keyType === "hash" ? field : undefined, value, ttlSeconds });
          setResult("Written.");
        }
      }
      onWritten();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : err instanceof Error ? err.message : "The write failed");
    } finally {
      setBusy(false);
    }
  }

  const modes: { key: Mode; label: string }[] = engine === "redis" || engine === "valkey"
    ? [{ key: "insert", label: "Set" }, { key: "delete", label: "Delete" }]
    : [{ key: "insert", label: "Insert" }, { key: "update", label: "Update" }, { key: "delete", label: "Delete" }];

  return (
    <div className="fixed inset-0 z-40 grid place-items-center bg-black/30 p-4" onClick={onClose}>
      <div className="flex max-h-[85vh] w-full max-w-lg flex-col gap-3 rounded-lg border border-slate-200 bg-white p-4 shadow-xl dark:border-slate-800 dark:bg-slate-900" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center justify-between">
          <h2 className="text-[13px] font-medium">Write {engine === "mongodb" ? "document" : engine === "elasticsearch" ? "document" : "key"}</h2>
          <button className="text-[12px] text-slate-500 hover:text-slate-800 dark:hover:text-slate-200" onClick={onClose}>Close</button>
        </div>
        {txnId && <p className="rounded bg-amber-50 px-2 py-1 text-[11px] text-amber-800 dark:bg-amber-950 dark:text-amber-300">Joins the open transaction — nothing is saved until you Commit.</p>}
        <div className="flex gap-1.5">{modes.map((m) => <button key={m.key} className={tabClass(mode === m.key)} onClick={() => { setMode(m.key); setError(""); setResult(""); }}>{m.label}</button>)}</div>

        {engine === "mongodb" && <input className={inputClass} placeholder="Collection" value={collection} onChange={(e) => setCollection(e.target.value)} />}
        {engine === "elasticsearch" && <div className="flex gap-2"><input className={inputClass} placeholder="Index" value={index} onChange={(e) => setIndex(e.target.value)} /><input className={inputClass} placeholder="Document _id" value={id} onChange={(e) => setId(e.target.value)} /></div>}

        {(engine === "redis" || engine === "valkey") && <div className="flex flex-col gap-2">
          <input className={inputClass} placeholder="Key" value={key} onChange={(e) => setKey(e.target.value)} />
          {mode !== "delete" && <>
            <div className="flex gap-1.5">
              <button className={tabClass(keyType === "string")} onClick={() => setKeyType("string")}>String</button>
              <button className={tabClass(keyType === "hash")} onClick={() => setKeyType("hash")}>Hash field</button>
            </div>
            {keyType === "hash" && <input className={inputClass} placeholder="Field" value={field} onChange={(e) => setField(e.target.value)} />}
            <textarea className={areaClass} style={{ minHeight: 80 }} placeholder="Value" value={value} onChange={(e) => setValue(e.target.value)} />
            <input className={inputClass} placeholder="TTL seconds (optional)" value={ttl} onChange={(e) => setTtl(e.target.value)} />
          </>}
        </div>}

        {engine !== "redis" && engine !== "valkey" && <div className="flex min-h-0 flex-1 flex-col gap-2">
          {mode === "insert" && <><label className="text-[11px] text-slate-500">Document</label><textarea className={areaClass} spellCheck={false} value={document} onChange={(e) => setDocument(e.target.value)} /></>}
          {mode === "update" && <>
            <label className="text-[11px] text-slate-500">Filter{engine === "mongodb" ? " (matches one document)" : ""}</label>
            {engine === "mongodb" && <textarea className={areaClass} style={{ minHeight: 70 }} spellCheck={false} value={filter} onChange={(e) => setFilter(e.target.value)} />}
            <label className="text-[11px] text-slate-500">{engine === "mongodb" ? "Update ($set, $unset, …)" : "Fields to merge"}</label>
            <textarea className={areaClass} spellCheck={false} value={update} onChange={(e) => setUpdate(e.target.value)} />
          </>}
          {mode === "delete" && engine === "mongodb" && <><label className="text-[11px] text-slate-500">Filter (matches one document)</label><textarea className={areaClass} spellCheck={false} value={filter} onChange={(e) => setFilter(e.target.value)} /></>}
        </div>}

        {error && <p className="text-[12px] text-rose-600 dark:text-rose-400">{error}</p>}
        {result && !error && <p className="text-[12px] text-emerald-600 dark:text-emerald-400">{result}</p>}
        <div className="flex justify-end gap-2">
          <button className="h-8 rounded-md border border-slate-200 px-3 text-[12px] text-slate-600 hover:bg-slate-50 dark:border-slate-800 dark:text-slate-300 dark:hover:bg-slate-800" onClick={onClose}>Cancel</button>
          <button disabled={busy} className="h-8 rounded-md bg-brand-600 px-3 text-[12px] font-medium text-white hover:bg-brand-700 disabled:cursor-not-allowed disabled:opacity-50" onClick={() => void run()}>{busy ? "Working…" : mode === "delete" ? "Delete" : mode === "update" ? "Update" : "Save"}</button>
        </div>
      </div>
    </div>
  );
}
