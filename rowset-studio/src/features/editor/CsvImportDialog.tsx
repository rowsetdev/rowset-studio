import { useState, type DragEvent } from "react";
import { Button, Modal, Select } from "../../components/ui";
import { Icon } from "../../components/Icon";
import PolicyBanner from "./PolicyBanner";
import { discardImport, runImport, startImport, uploadImportChunk, type TableInfo } from "./api";
import { detectDelimiter, parseCsv, type Delimiter } from "./csv";

const PREVIEW_BYTES = 256 * 1024;
const CHUNK_BYTES = 512 * 1024;
const DELIMITER_LABELS: Record<Delimiter, string> = { ",": "Comma", ";": "Semicolon", "\t": "Tab", "|": "Pipe" };

type Phase = "choose" | "uploading" | "importing" | "done" | "error";

function fileSize(bytes: number) {
  return bytes < 1024 * 1024 ? `${Math.max(1, Math.round(bytes / 1024))} KB` : `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

// Imports a CSV file into an existing table. The file uploads in chunks and
// is inserted in one transaction, so either every row lands or none does.
export default function CsvImportDialog({ connectionId, database, schemaName, table, engine, onClose }: { connectionId: string; database?: string; schemaName: string; table: TableInfo; engine?: string; onClose: () => void }) {
  const [file, setFile] = useState<File | null>(null);
  const [text, setText] = useState("");
  const [truncated, setTruncated] = useState(false);
  const [delimiter, setDelimiter] = useState<Delimiter>(",");
  const [header, setHeader] = useState(true);
  const [nullEmpty, setNullEmpty] = useState(true);
  const [mapping, setMapping] = useState<string[]>([]);
  const [phase, setPhase] = useState<Phase>("choose");
  const [progress, setProgress] = useState(0);
  const [imported, setImported] = useState<{ rows: number; durationMs: number } | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [dragging, setDragging] = useState(false);

  const target = `${schemaName ? `${schemaName}.` : ""}${table.name}`;
  const rows = text ? parseCsv(text, delimiter, 51) : [];
  const previewRows = truncated && rows.length === 51 ? rows.slice(0, 50) : rows;
  const width = Math.max(0, ...previewRows.map((row) => row.length));
  const headings = header && previewRows[0] ? previewRows[0] : Array.from({ length: width }, (_, index) => `Column ${index + 1}`);
  const sample = header ? previewRows.slice(1, 7) : previewRows.slice(0, 6);
  const used = mapping.filter(Boolean);
  const duplicate = used.find((name, index) => used.indexOf(name) !== index);
  const busy = phase === "uploading" || phase === "importing";

  function autoMap(nextRows: string[][], withHeader: boolean) {
    const columns = table.columns.map((column) => column.name);
    const count = Math.max(0, ...nextRows.map((row) => row.length));
    setMapping(Array.from({ length: count }, (_, index) => {
      if (withHeader) {
        const name = (nextRows[0]?.[index] ?? "").trim().toLowerCase();
        return columns.find((column) => column.toLowerCase() === name) ?? "";
      }
      return columns[index] ?? "";
    }));
  }

  async function choose(next: File | undefined) {
    if (!next) return;
    const head = await next.slice(0, PREVIEW_BYTES).text();
    const detected = detectDelimiter(head);
    setFile(next);
    setText(head);
    setTruncated(next.size > PREVIEW_BYTES);
    setDelimiter(detected);
    setPhase("choose");
    setError(null);
    autoMap(parseCsv(head, detected, 51), header);
  }

  function drop(event: DragEvent) {
    event.preventDefault();
    setDragging(false);
    if (!busy) void choose(event.dataTransfer.files?.[0]);
  }

  async function importFile() {
    if (!file) return;
    setError(null);
    setProgress(0);
    setPhase("uploading");
    let importId = "";
    try {
      importId = (await startImport(connectionId)).importId;
      for (let offset = 0; offset < file.size; offset += CHUNK_BYTES) {
        await uploadImportChunk(connectionId, importId, file.slice(offset, offset + CHUNK_BYTES));
        setProgress(Math.min(100, Math.round(((offset + CHUNK_BYTES) / file.size) * 100)));
      }
      setPhase("importing");
      const result = await runImport(connectionId, importId, {
        schema: schemaName,
        table: table.name,
        database: database ?? "",
        header,
        delimiter: delimiter === "\t" ? "\\t" : delimiter,
        nullEmpty,
        columns: mapping.map((column, source) => ({ source, target: column })).filter((column) => column.target),
      });
      setImported(result);
      setPhase("done");
    } catch (err) {
      if (importId) void discardImport(connectionId, importId).catch(() => undefined);
      setError(err);
      setPhase("error");
    }
  }

  if (phase === "done" && imported) {
    return (
      <Modal title="Import CSV" onClose={onClose}>
        <div className="flex flex-col items-center gap-3 py-4 text-center">
          <span className="grid h-11 w-11 place-items-center rounded-full bg-emerald-50 text-emerald-600 ring-1 ring-emerald-200 dark:bg-emerald-500/10 dark:text-emerald-300 dark:ring-emerald-500/30"><Icon name="check" size={20} /></span>
          <div>
            <p className="text-[14px] font-semibold text-slate-900 dark:text-slate-100">{imported.rows.toLocaleString()} row{imported.rows === 1 ? "" : "s"} imported</p>
            <p className="mt-0.5 text-[12px] text-slate-500">into <code className="font-mono">{target}</code> in {(imported.durationMs / 1000).toFixed(1)} s</p>
          </div>
          <Button onClick={onClose}>Done</Button>
        </div>
      </Modal>
    );
  }

  return (
    <Modal title="Import CSV" onClose={() => !busy && onClose()} size="xl" closeOnBackdrop={false}>
      <div className="space-y-4 text-[13px]">
        <p className="-mt-2 text-[12px] text-slate-500">
          Into <code className="rounded bg-slate-100 px-1 py-0.5 font-mono text-slate-700 dark:bg-slate-800 dark:text-slate-200">{target}</code>. {engine === "cassandra" ? "Rows are written in logged batches of up to 50; completed batches are not rolled back if a later batch fails." : "Rows are added in one transaction: all of them or none."}
        </p>

        {!file ? (
          <label
            onDragOver={(event) => { event.preventDefault(); setDragging(true); }}
            onDragLeave={() => setDragging(false)}
            onDrop={drop}
            className={`flex cursor-pointer flex-col items-center justify-center gap-2 rounded-lg border border-dashed px-6 py-10 text-center transition ${dragging ? "border-brand-400 bg-brand-50/60 dark:bg-brand-500/10" : "border-slate-300 bg-slate-50/60 hover:border-brand-300 dark:border-slate-700 dark:bg-slate-900/40"}`}
          >
            <span className="grid h-10 w-10 place-items-center rounded-full bg-white text-slate-500 shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:ring-slate-700"><Icon name="upload" size={17} /></span>
            <span className="text-[13px] font-medium text-slate-700 dark:text-slate-200">Drop a CSV file here, or <span className="text-brand-600 dark:text-brand-400">browse</span></span>
            <span className="text-[11px] text-slate-400">.csv, .tsv or .txt · up to 256 MB</span>
            <input type="file" accept=".csv,.tsv,.txt,text/csv" className="sr-only" onChange={(event) => void choose(event.target.files?.[0])} />
          </label>
        ) : (
          <>
            <div className="flex flex-wrap items-center gap-3 rounded-lg border border-slate-200 px-3 py-2.5 dark:border-slate-800">
              <span className="grid h-8 w-8 shrink-0 place-items-center rounded-md bg-slate-100 text-slate-500 dark:bg-slate-800"><Icon name="text" size={15} /></span>
              <div className="min-w-0 flex-1">
                <div className="truncate font-medium text-slate-800 dark:text-slate-100">{file.name}</div>
                <div className="text-[11px] text-slate-500">{fileSize(file.size)} · {width} column{width === 1 ? "" : "s"}</div>
              </div>
              <label className={`inline-flex h-8 cursor-pointer items-center rounded-md border border-slate-200 px-3 text-[12px] text-slate-600 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800 ${busy ? "pointer-events-none opacity-50" : ""}`}>
                Change file
                <input type="file" accept=".csv,.tsv,.txt,text/csv" className="sr-only" onChange={(event) => void choose(event.target.files?.[0])} />
              </label>
            </div>

            <div className="flex flex-wrap items-center gap-x-6 gap-y-3">
              <div className="flex items-center gap-2">
                <span className="text-[12px] font-medium text-slate-600 dark:text-slate-400">Delimiter</span>
                <Select className="w-32" value={delimiter} disabled={busy} onChange={(event) => { const next = event.target.value as Delimiter; setDelimiter(next); autoMap(parseCsv(text, next, 51), header); }}>
                  {(Object.keys(DELIMITER_LABELS) as Delimiter[]).map((key) => <option key={key} value={key}>{DELIMITER_LABELS[key]}</option>)}
                </Select>
              </div>
              <Switch label="First row is a header" on={header} disabled={busy} onChange={(next) => { setHeader(next); autoMap(rows, next); }} />
              <Switch label="Empty values become NULL" on={nullEmpty} disabled={busy} onChange={setNullEmpty} />
            </div>

            <div>
              <div className="mb-1.5 flex items-baseline justify-between">
                <h3 className="text-[12px] font-semibold text-slate-700 dark:text-slate-200">Columns</h3>
                <span className="text-[11px] text-slate-500">{used.length} of {width} mapped · unmapped table columns get their defaults</span>
              </div>
              <div className="overflow-x-auto rounded-lg border border-slate-200 dark:border-slate-800">
                <table className="min-w-full text-left text-[12px]">
                  <thead className="bg-[#f3f4f6] text-slate-500 dark:bg-slate-900 dark:text-slate-400">
                    <tr>
                      {headings.map((heading, index) => (
                        <th key={index} className="min-w-[160px] border-b border-r border-slate-200 px-2 py-2 align-top font-medium last:border-r-0 dark:border-slate-800">
                          <div className="mb-1.5 truncate text-slate-700 dark:text-slate-200" title={heading}>{heading || `Column ${index + 1}`}</div>
                          <Select value={mapping[index] ?? ""} disabled={busy} onChange={(event) => setMapping((current) => current.map((value, i) => (i === index ? event.target.value : value)))}>
                            <option value="">Skip</option>
                            {table.columns.map((column) => <option key={column.name} value={column.name}>{column.name}</option>)}
                          </Select>
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                    {sample.map((row, rowIndex) => (
                      <tr key={rowIndex} className="h-7">
                        {headings.map((_, index) => (
                          <td key={index} className={`max-w-[240px] truncate border-r border-slate-100 px-2 font-mono last:border-r-0 dark:border-slate-800 ${mapping[index] ? "text-slate-700 dark:text-slate-200" : "text-slate-400"}`}>{row[index] ?? ""}</td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>

            {duplicate && <p className="text-[12px] text-rose-600 dark:text-rose-400">{duplicate} is chosen for more than one CSV column.</p>}
            {phase === "error" && <PolicyBanner error={error} />}

            <div className="flex items-center gap-3 border-t border-slate-200 pt-3 dark:border-slate-800">
              {busy ? (
                <div className="flex min-w-0 flex-1 items-center gap-2">
                  <div className="h-1.5 w-40 overflow-hidden rounded-full bg-slate-100 dark:bg-slate-800">
                    <div className={`h-full rounded-full bg-brand-500 transition-all ${phase === "importing" ? "animate-pulse" : ""}`} style={{ width: `${phase === "importing" ? 100 : progress}%` }} />
                  </div>
                  <span className="text-[12px] text-slate-500">{phase === "uploading" ? `Uploading ${progress}%` : engine === "cassandra" ? "Importing in logged batches…" : "Importing in one transaction…"}</span>
                </div>
              ) : <span className="flex-1" />}
              <button type="button" onClick={onClose} disabled={busy} className="inline-flex h-8 items-center rounded-md border border-slate-200 bg-white px-3 text-[13px] font-medium text-slate-600 transition hover:bg-slate-50 disabled:opacity-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800">Cancel</button>
              <Button onClick={() => void importFile()} disabled={busy || !used.length || Boolean(duplicate)}>Import</Button>
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}

function Switch({ label, on, disabled, onChange }: { label: string; on: boolean; disabled?: boolean; onChange: (on: boolean) => void }) {
  return (
    <button type="button" role="switch" aria-checked={on} disabled={disabled} onClick={() => onChange(!on)} className="inline-flex items-center gap-2 text-[12px] text-slate-700 disabled:opacity-50 dark:text-slate-200">
      <span className={`relative h-4 w-7 shrink-0 rounded-full transition-colors ${on ? "bg-emerald-500" : "bg-slate-300 dark:bg-slate-700"}`}>
        <span className={`absolute top-0.5 h-3 w-3 rounded-full bg-white shadow transition-all ${on ? "left-3.5" : "left-0.5"}`} />
      </span>
      {label}
    </button>
  );
}
