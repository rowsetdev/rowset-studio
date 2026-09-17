import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useSearchParams } from "react-router";
import { Badge, Button, ErrorText, Field, Input, PageHeader, Panel, Select } from "../../components/ui";
import { useConnections } from "../connections/useConnections";
import { getSchema, listDatabases, objectDDL, forgetSchema, type SchemaNode } from "./api";
import { compareSchemas, migrationDraft, type SchemaDifference } from "./schemaCompare";
import SqlCode from "./SqlCode";
import { useEngineCapabilities } from "../../lib/instance";

interface Endpoint { connection: string; database: string; schema: string }
const empty: Endpoint = { connection: "", database: "", schema: "" };
const emptySchema: SchemaNode = { name: "", tables: [] };
// These engines have no table/column metadata a schema diff can compare.
const unsupportedCompareEngines = new Set(["mongodb", "redis", "valkey", "elasticsearch"]);

export default function SchemaComparePage() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data: connections = [] } = useConnections();
  const [source, setSource] = useState<Endpoint>({ ...empty, connection: params.get("connection") ?? "", database: params.get("database") ?? "" });
  const [target, setTarget] = useState<Endpoint>(empty);
  const [filter, setFilter] = useState("");
  const [selected, setSelected] = useState("");
  const [notice, setNotice] = useState("");
  const [draft, setDraft] = useState(false);
  const left = useQuery({ queryKey: ["schema", source.connection, source.database], queryFn: () => getSchema(source.connection, source.database), enabled: !!source.connection });
  const right = useQuery({ queryKey: ["schema", target.connection, target.database], queryFn: () => getSchema(target.connection, target.database), enabled: !!target.connection });
  const a = left.data?.schemas.find(s => s.name === source.schema) ?? left.data?.schemas[0];
  const b = right.data?.schemas.find(s => s.name === target.schema) ?? right.data?.schemas[0];
  const sourceEngine = connections.find(c => c.id === source.connection)?.engine;
  const targetEngine = connections.find(c => c.id === target.connection)?.engine;
  const sameEngine = !!sourceEngine && sourceEngine === targetEngine;
  const ready = !!a && !!b && !left.isFetching && !right.isFetching && !left.isError && !right.isError;
  const differences = useMemo(() => compareSchemas(a ?? emptySchema, b ?? emptySchema), [a, b]);
  const visible = differences.filter(d => `${d.table} ${d.object} ${d.status}`.toLowerCase().includes(filter.toLowerCase()));
  const current = visible.find(d => d.id === selected) ?? visible[0];
  const warnings = [...(left.data?.warnings ?? []), ...(right.data?.warnings ?? [])];
  const sql = migrationDraft(targetEngine ?? "", a?.name ?? "", b?.name ?? "", differences);
  const changeEndpoint = (side: "source" | "target", value: Endpoint) => { (side === "source" ? setSource : setTarget)(value); setSelected(""); setDraft(false); setNotice(""); };

  async function refresh() {
    setNotice("");
    try {
      await Promise.all([...new Set([source.connection, target.connection].filter(Boolean))].map(forgetSchema));
      await queryClient.invalidateQueries({ queryKey: ["compare-ddl"] });
      await Promise.all([source.connection ? left.refetch() : null, target.connection ? right.refetch() : null]);
    } catch (error) { setNotice(error instanceof Error ? error.message : "Could not refresh schemas."); }
  }

  return <div className="flex h-full min-h-0 flex-col gap-3">
    <PageHeader icon="table" title="Schema comparison" subtitle="Compare table structure. Source is the desired structure; changes describe what the target needs." actions={<Button onClick={() => void refresh()} disabled={left.isFetching || right.isFetching || !source.connection && !target.connection}>Refresh</Button>} />
    <div className="grid gap-3 lg:grid-cols-2">
      <EndpointPicker label="Source" value={source} onChange={v => changeEndpoint("source", v)} schemas={left.data?.schemas ?? []} />
      <EndpointPicker label="Target" value={target} onChange={v => changeEndpoint("target", v)} schemas={right.data?.schemas ?? []} />
    </div>
    <ErrorText>{notice || (left.isError ? `Source: ${left.error.message}` : right.isError ? `Target: ${right.error.message}` : "")}</ErrorText>
    {warnings.length > 0 && <Panel className="p-3 text-[12px] text-amber-700 dark:text-amber-300">Metadata is incomplete. Missing objects may be permission or timeout errors. Migration draft is disabled.<ul className="mt-1 list-inside list-disc">{warnings.map((w, i) => <li key={i}>{w}</li>)}</ul></Panel>}
    {!source.connection || !target.connection ? <Panel className="p-8 text-center text-[13px] text-slate-500">Choose a source and a target connection to compare their schemas.</Panel> : left.isFetching || right.isFetching ? <Panel className="p-8 text-center text-[13px] text-slate-500">Reading schema metadata…</Panel> : ready ? <>
      <Panel className="flex flex-wrap items-center gap-3 p-2.5">
        <Input className="max-w-xs" aria-label="Filter differences" placeholder="Filter tables, columns and indexes…" value={filter} onChange={e => setFilter(e.target.value)} />
        <span className="text-[12px] text-slate-500">{differences.length} differences</span>
        {(["add", "change", "remove"] as const).map(status => <Badge key={status} tone={status === "add" ? "success" : status === "remove" ? "danger" : "warn"}>{differences.filter(d => d.status === status).length} {status}</Badge>)}
        <button className="ml-auto text-[12px] text-slate-500 hover:text-brand-600" onClick={() => { setSource(target); setTarget(source); setSelected(""); setDraft(false); }}>Swap source and target</button>
        <Button disabled={!sameEngine || !!warnings.length || !differences.length} onClick={() => setDraft(!draft)}>{draft ? "Show differences" : "Migration draft"}</Button>
      </Panel>
      {!sameEngine && <p className="text-[12px] text-amber-600">Different engines: types are compared as reported. Migration SQL requires the same engine.</p>}
      <p className="text-[11px] text-slate-500">Compares columns, defaults, generated metadata, primary-key flags, reported foreign-key references and index summaries. CHECK constraints, full index/FK definitions, views and routines require DDL review.</p>
      {draft ? <Panel className="flex min-h-0 flex-1 flex-col"><div className="flex items-center gap-3 border-b border-slate-200 p-3 dark:border-slate-800"><span className="flex-1 text-[12px] text-slate-500">Only supported nullable column additions are generated. Other changes are marked MANUAL.</span><Button onClick={() => navigate("/editor", { state: { openSql: sql, connectionId: target.connection, database: target.database, title: "Schema migration" } })}>Open in editor</Button><button className="text-[12px] text-slate-500" onClick={() => { void navigator.clipboard.writeText(sql).then(() => setNotice("Copied migration draft."), () => setNotice("Clipboard unavailable. Open the draft in the editor to copy it.")); }}>Copy</button></div><SqlCode sql={sql} className="min-h-0 flex-1 p-3" /></Panel> : differences.length === 0 ? <Panel className="p-8 text-center text-[13px] text-slate-500">No differences in the compared metadata.</Panel> : <div className="grid min-h-0 flex-1 gap-3 xl:grid-cols-[minmax(280px,1fr)_2fr]">
        <Panel className="min-h-0 overflow-auto"><ul className="divide-y divide-slate-100 dark:divide-slate-800">{visible.map(d => <li key={d.id}><button onClick={() => setSelected(d.id)} className={`flex w-full items-center gap-2 px-3 py-2.5 text-left text-[12px] hover:bg-slate-50 dark:hover:bg-slate-900 ${current?.id === d.id ? "bg-slate-100 dark:bg-slate-900" : ""}`}><Badge tone={d.status === "add" ? "success" : d.status === "remove" ? "danger" : "warn"}>{d.status}</Badge><span className="min-w-0 flex-1 truncate">{d.table}{d.kind !== "table" ? `.${d.object}` : ""}</span><span className="text-[11px] text-slate-400">{d.kind}</span></button></li>)}</ul>{!visible.length && <p className="p-4 text-[12px] text-slate-500">No matching differences.</p>}</Panel>
        {current && <DifferenceDetail key={current.id + source.connection + target.connection + source.database + target.database + a.name + b.name} difference={current} source={{ ...source, schema: a.name }} target={{ ...target, schema: b.name }} sourceEngine={sourceEngine} targetEngine={targetEngine} />}
      </div>}
    </> : !left.isError && !right.isError ? <Panel className="p-8 text-center text-[13px] text-slate-500">No schema metadata is available for one of these databases.</Panel> : null}
  </div>;
}

function EndpointPicker({ label, value, onChange, schemas }: { label: string; value: Endpoint; onChange: (v: Endpoint) => void; schemas: SchemaNode[] }) {
  const { data: connections = [] } = useConnections();
  const { data: databases = [] } = useQuery({ queryKey: ["databases", value.connection], queryFn: () => listDatabases(value.connection), enabled: !!value.connection });
  return <Panel className="space-y-2 p-3"><h2 className="text-[13px] font-medium">{label}</h2><div className="grid gap-2 sm:grid-cols-3"><Field label="Connection"><Select value={value.connection} onChange={e => onChange({ connection: e.target.value, database: connections.find(c => c.id === e.target.value)?.database ?? "", schema: "" })}><option value="">Choose connection</option>{connections.filter(c => !unsupportedCompareEngines.has(c.engine as string)).map(c => <option key={c.id} value={c.id}>{c.name}</option>)}</Select></Field><Field label="Database"><Select disabled={!value.connection} value={value.database} onChange={e => onChange({ ...value, database: e.target.value, schema: "" })}>{[...new Set([value.database, ...databases])].map(d => <option key={d} value={d}>{d || "Default database"}</option>)}</Select></Field><Field label="Schema"><Select disabled={!schemas.length} value={value.schema || schemas[0]?.name || ""} onChange={e => onChange({ ...value, schema: e.target.value })}>{schemas.map(s => <option key={s.name} value={s.name}>{s.name}</option>)}</Select></Field></div></Panel>;
}

function DifferenceDetail({ difference: d, source, target, sourceEngine, targetEngine }: { difference: SchemaDifference; source: Endpoint; target: Endpoint; sourceEngine?: string; targetEngine?: string }) {
  const [ddl, setDDL] = useState(false);
  const sourceDDL = useEngineCapabilities(sourceEngine).ddl;
  const targetDDL = useEngineCapabilities(targetEngine).ddl;
  const load = (side: Endpoint) => objectDDL(side.connection, { database: side.database, schema: side.schema === "default" ? "" : side.schema, kind: "table", name: d.table });
  const a = useQuery({ queryKey: ["compare-ddl", source, d.table], queryFn: () => load(source), enabled: ddl && sourceDDL && !(d.kind === "table" && d.status === "remove") });
  const b = useQuery({ queryKey: ["compare-ddl", target, d.table], queryFn: () => load(target), enabled: ddl && targetDDL && !(d.kind === "table" && d.status === "add") });
  return <Panel className="flex min-h-0 flex-col overflow-auto"><div className="flex items-center justify-between border-b border-slate-200 p-3 dark:border-slate-800"><span className="text-[13px] font-medium">{d.table} · {d.object}</span>{(sourceDDL || targetDDL) && <button onClick={() => setDDL(!ddl)} className="text-[12px] text-brand-600">{ddl ? "Show metadata" : "Compare table DDL"}</button>}</div><div className="grid min-h-0 flex-1 divide-y divide-slate-200 lg:grid-cols-2 lg:divide-x lg:divide-y-0 dark:divide-slate-800">{[{ label: "Source", text: d.source, query: a, supported: sourceDDL }, { label: "Target", text: d.target, query: b, supported: targetDDL }].map(side => <div key={side.label} className="min-w-0 overflow-auto p-3"><h3 className="mb-3 text-[11px] font-medium uppercase tracking-wide text-slate-400">{side.label}</h3>{ddl ? !side.supported ? <p className="text-[12px] text-slate-500">DDL is unavailable for this engine.</p> : side.query.isFetching ? <p className="text-[12px]">Loading definition…</p> : side.query.isError ? <ErrorText>{side.query.error.message}</ErrorText> : <SqlCode sql={side.query.data ?? "-- Table is absent."} /> : <p className="whitespace-pre-wrap break-words font-mono text-[12px] leading-6">{side.text}</p>}</div>)}</div></Panel>;
}
