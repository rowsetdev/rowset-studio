import { useContext, useEffect, useState } from "react";
import { Icon, type IconName } from "../../components/Icon";
import { ApiError } from "../../lib/api";
import { useSchema } from "./useEditor";
import { ColumnInfo, RoutineInfo, TableInfo, TriggerInfo, exportTable, objectDDL } from "./api";
import { SchemaActions, quoteIdentifier, tableSelect } from "./schemaActions";
import CsvImportDialog from "./CsvImportDialog";
import { useEngineCapabilities } from "../../lib/instance";
import { useConnections } from "../connections/useConnections";
import SqlCode from "./SqlCode";
import { formatDefinition } from "./ddlFormat";
import RowMenu from "../../components/RowMenu";
import { Button, Modal } from "../../components/ui";

// Shorten verbose SQL type names so long ones don't crowd out the column name.
function shortType(t: string) {
  const map: Record<string, string> = {
    "timestamp without time zone": "timestamp",
    "timestamp with time zone": "timestamptz",
    "time without time zone": "time",
    "character varying": "varchar",
    "double precision": "double",
    "character": "char",
  };
  return map[t.toLowerCase()] ?? t;
}

function colTitle(c: ColumnInfo) {
  const parts = [`${c.name} ${c.dataType}`, c.nullable ? "nullable" : "not null"];
  if (c.pk) parts.push("primary key");
  if (c.references) parts.push(`→ ${c.references}`);
  if (c.default) parts.push(`default: ${c.default}`);
  if (c.generated) parts.push(c.generated);
  if (c.comment) parts.push(c.comment);
  return parts.join(" · ");
}

function qualifyName(engine: string, schemaName: string, objectName: string) {
  if (engine === "mysql" || engine === "mariadb") return objectName;
  return schemaName && schemaName !== "default" ? `${schemaName}.${objectName}` : objectName;
}

type QualifiedTable = {
  key: string;
  schemaName: string;
  table: TableInfo;
};

type QualifiedRoutine = {
  key: string;
  schemaName: string;
  routine: RoutineInfo;
};

type QualifiedTrigger = {
  key: string;
  schemaName: string;
  trigger: TriggerInfo;
};

// Lazy schema tree rendered as a flat database object list. Schemas stay in the
// object names (`schema.table`) instead of taking their own tree level.
export default function SchemaBrowser({
  connectionId,
  database,
  engine = "",
  compact = false,
  search = "",
}: {
  connectionId: string | null;
  database?: string;
  engine?: string;
  compact?: boolean;
  /** Search text from the explorer; the database's own filter wins when set. */
  search?: string;
}) {
  const { data, isLoading, isError, error, refetch, isFetching } = useSchema(connectionId, database);
  const { data: connections = [] } = useConnections();
  const readOnly = connections.some((connection) => connection.id === connectionId && (connection.readOnly || connection.defaultNodeRole === "secondary"));
  const [filter, setFilter] = useState("");
  const [schemaFilter, setSchemaFilter] = useState("");

  if (!connectionId) return <Hint>Select a connection.</Hint>;
  if (isLoading) return <Hint>Loading schema…</Hint>;
  if (isError) return (
    <div className="space-y-1.5 px-1 text-xs text-rose-600 dark:text-rose-400">
      <p title={schemaError(error)}>Schema unavailable: {schemaError(error)}</p>
      <button type="button" onClick={() => void refetch()} disabled={isFetching} className="rounded border border-rose-200 px-2 py-1 text-[11px] font-medium hover:bg-rose-50 disabled:opacity-50 dark:border-rose-900 dark:hover:bg-rose-950">
        {isFetching ? "Retrying…" : "Retry"}
      </button>
    </div>
  );
  if (!data) return null;

  return (
    <div className={compact ? "space-y-1 text-[13px]" : "space-y-3 text-[13px]"}>
      {(() => {
        const tables: QualifiedTable[] = [];
        const views: QualifiedTable[] = [];
        const procedures: QualifiedRoutine[] = [];
        const functions: QualifiedRoutine[] = [];
        const triggers: QualifiedTrigger[] = [];
        for (const schema of data.schemas ?? []) {
          if (schemaFilter && schema.name !== schemaFilter) continue;
          for (const table of schema.tables ?? []) {
            tables.push({
              key: qualifyName(engine, schema.name, table.name),
              schemaName: schema.name,
              table,
            });
          }
          for (const view of schema.views ?? []) {
            views.push({
              key: qualifyName(engine, schema.name, view.name),
              schemaName: schema.name,
              table: view,
            });
          }
          for (const routine of schema.routines ?? []) {
            const item = {
              key: qualifyName(engine, schema.name, routine.name),
              schemaName: schema.name,
              routine,
            };
            if (routine.kind === "procedure") procedures.push(item);
            else functions.push(item);
          }
          for (const trigger of schema.triggers ?? []) {
            triggers.push({
              key: `${qualifyName(engine, schema.name, trigger.table)}.${trigger.name}`,
              schemaName: schema.name,
              trigger,
            });
          }
        }

        // A trigger belongs under the table or view it fires on; only one the
        // schema has no such table for stays in the group of its own.
        const tableTriggers = new Map<string, TriggerInfo[]>();
        const objectKey = (schemaName: string, name: string) => `${schemaName}|${name.toLowerCase()}`;
        const owners = new Set([...tables, ...views].map((item) => objectKey(item.schemaName, item.table.name)));
        const looseTriggers = triggers.filter((item) => {
          const owner = objectKey(item.schemaName, item.trigger.table ?? "");
          if (!item.trigger.table || !owners.has(owner)) return true;
          tableTriggers.set(owner, [...(tableTriggers.get(owner) ?? []), item.trigger]);
          return false;
        });
        const triggersOf = (item: QualifiedTable) => tableTriggers.get(objectKey(item.schemaName, item.table.name));

        const needle = (filter || search).trim().toLowerCase();
        const matches = (key: string) => !needle || key.toLowerCase().includes(needle);
        const visibleViews = views.filter(t => matches(t.key) || t.table.columns.some(c => matches(c.name)) || (triggersOf(t) ?? []).some(trigger => matches(trigger.name)));
        const visibleProcedures = procedures.filter(r => matches(r.key));
        const visibleFunctions = functions.filter(r => matches(r.key));
        const visibleTriggers = looseTriggers.filter(t => matches(t.key));
        const sequences: { key: string; name: string; schemaName: string }[] = [];
        for (const schema of data.schemas ?? []) {
          if (schemaFilter && schema.name !== schemaFilter) continue;
          for (const sequence of schema.sequences ?? []) sequences.push({ key: qualifyName(engine, schema.name, sequence.name), name: sequence.name, schemaName: schema.name });
        }
        const visibleSequences = sequences.filter(s => matches(s.key));
        const filteredTables = tables
          .filter((item) => !needle || item.key.toLowerCase().includes(needle) || (item.table.columns ?? []).some((column) => column.name.toLowerCase().includes(needle)) || (triggersOf(item) ?? []).some((trigger) => trigger.name.toLowerCase().includes(needle)))
          .sort((left, right) => left.key.localeCompare(right.key));

        return (
          <>
            <div className="mb-1 flex items-center gap-1">
              <div className="relative min-w-0 flex-1">
                <Icon name="search" size={13} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-slate-400" />
                <input
                  value={filter}
                  onChange={(event) => setFilter(event.target.value)}
                  placeholder={search && !filter ? `Filtered by “${search}”` : "Filter tables and columns…"}
                  title="Search tables, views, routines and column names"
                  aria-label="Search objects or columns"
                  className="h-7 w-full rounded-md border border-slate-200 bg-white pl-8 pr-8 text-[12px] text-slate-700 outline-none transition placeholder:text-slate-400 focus:border-slate-400 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-200"
                />
                <button
                  type="button"
                  title={isFetching ? "Refreshing schema…" : "Refresh schema"}
                  aria-label="Refresh schema"
                  disabled={isFetching}
                  onClick={() => void refetch()}
                  className="absolute right-1 top-1/2 grid h-5 w-5 -translate-y-1/2 place-items-center rounded text-slate-400 hover:bg-slate-100 hover:text-slate-700 disabled:cursor-wait dark:hover:bg-slate-900 dark:hover:text-slate-200"
                >
                  <Icon name="refresh" size={12} className={isFetching ? "animate-spin" : ""} />
                </button>
              </div>
              {((data.schemas?.length ?? 0) > 1 || schemaFilter) && (
                <select aria-label="Filter schema" title="Schema" className="h-7 w-24 shrink-0 truncate rounded-md border border-slate-200 bg-white px-1.5 text-[12px] text-slate-700 outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-slate-200" value={schemaFilter} onChange={e => setSchemaFilter(e.target.value)}>
                  <option value="">All schemas</option>
                  {(data.schemas ?? []).map(s => <option key={s.name} value={s.name}>{s.name}</option>)}
                </select>
              )}
            </div>
            {(data.warnings?.length ?? 0) > 0 && (
              <div className="mb-2 rounded border border-amber-200 bg-amber-50 px-2 py-1.5 text-[11px] leading-4 text-amber-800 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-300" title={data.warnings?.join("\n")}>
                Tables and columns loaded. Some optional object metadata is unavailable.
              </div>
            )}
            <ObjectGroup label={engine === "mongodb" ? "Collections" : "Tables"} count={filteredTables.length === tables.length ? `${tables.length}` : `${filteredTables.length} of ${tables.length}`} forceOpen={Boolean(needle)}>
              {filteredTables.map((t) => (
                <TableItem key={t.key} engine={engine} schemaName={t.schemaName} table={t.table} triggers={triggersOf(t)} connectionId={connectionId} database={database} readOnly={readOnly} />
              ))}
              {filteredTables.length === 0 && <li className="px-1 py-1 text-[11px] text-slate-400">No matching tables.</li>}
            </ObjectGroup>
            {visibleViews.length > 0 && (
              <ObjectGroup label="Views" count={visibleViews.length}>
                {visibleViews.map((t) => (
                  <TableItem key={t.key} engine={engine} schemaName={t.schemaName} table={t.table} triggers={triggersOf(t)} icon="grid" connectionId={connectionId} database={database} readOnly={readOnly} />
                ))}
              </ObjectGroup>
            )}
            {visibleProcedures.length > 0 && (
              <ObjectGroup label="Procedures" count={visibleProcedures.length}>
                {visibleProcedures.map((r) => (
                  <RoutineItem key={r.key} engine={engine} schemaName={r.schemaName} routine={r.routine} connectionId={connectionId} database={database} />
                ))}
              </ObjectGroup>
            )}
            {visibleFunctions.length > 0 && (
              <ObjectGroup label="Functions" count={visibleFunctions.length}>
                {visibleFunctions.map((r) => (
                  <RoutineItem key={r.key} engine={engine} schemaName={r.schemaName} routine={r.routine} connectionId={connectionId} database={database} />
                ))}
              </ObjectGroup>
            )}
            {visibleSequences.length > 0 && (
              <ObjectGroup label="Sequences" count={visibleSequences.length}>
                {visibleSequences.map((s) => (
                  <ObjectRow key={s.key} icon="sort" label={s.key} title={`sequence ${s.key}`} engine={engine} connectionId={connectionId} database={database} schemaName={s.schemaName} kind="sequence" name={s.name} />
                ))}
              </ObjectGroup>
            )}
            {visibleTriggers.length > 0 && (
              <ObjectGroup label="Triggers" count={visibleTriggers.length}>
                {visibleTriggers.map((t) => (
                  <TriggerItem key={t.key} engine={engine} schemaName={t.schemaName} trigger={t.trigger} connectionId={connectionId} database={database} />
                ))}
              </ObjectGroup>
            )}
          </>
        );
      })()}
    </div>
  );
}

// Shows what an object is, as the database itself describes it.
function DDLViewer({ engine, connectionId, database, schemaName, kind, name, onClose }: { engine: string; connectionId: string; database?: string; schemaName: string; kind: string; name: string; onClose: () => void }) {
  const action = useContext(SchemaActions);
  const [sql, setSql] = useState("");
  const [error, setError] = useState("");
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    let alive = true;
    objectDDL(connectionId, { database, schema: schemaName, kind, name })
      // A definition the database returns on one line is laid out on lines;
      // Copy and Open in editor take the text as shown.
      .then((text) => { if (alive) setSql(formatDefinition(text, engine.toLowerCase())); }, (err: unknown) => { if (alive) setError(schemaError(err)); });
    return () => { alive = false; };
  }, [engine, connectionId, database, schemaName, kind, name]);
  return (
    <Modal title={`${kind.charAt(0).toUpperCase()}${kind.slice(1)}: ${name}`} onClose={onClose} size="lg">
      {error ? (
        <p role="alert" className="text-[13px] text-rose-600 dark:text-rose-400">{error}</p>
      ) : (
        <SqlCode sql={sql || "-- Loading…"} className="max-h-[60vh] rounded-md border border-slate-200 bg-slate-50 p-3 dark:border-slate-800 dark:bg-slate-900" />
      )}
      <div className="mt-4 flex justify-end gap-2">
        <button type="button" disabled={!sql} onClick={() => void navigator.clipboard.writeText(sql).then(() => { setCopied(true); window.setTimeout(() => setCopied(false), 1500); }, () => undefined)} className="h-8 rounded-md border border-slate-200 px-3 text-[13px] text-slate-600 hover:bg-slate-50 disabled:opacity-50 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800">
          {copied ? "Copied" : "Copy"}
        </button>
        <Button disabled={!sql} onClick={() => { action({ connectionId, database, sql }); onClose(); }}>Open in editor</Button>
      </div>
    </Modal>
  );
}

// One non-table object: its name, and a menu that shows its definition.
function ObjectRow({ icon, label, title, engine, connectionId, database, schemaName, kind, name, trailing }: { icon: IconName; label: string; title: string; engine: string; connectionId: string; database?: string; schemaName: string; kind: string; name: string; trailing?: React.ReactNode }) {
  const [showing, setShowing] = useState(false);
  return (
    <li className="group flex h-6 items-center gap-2 rounded px-1 pl-[22px] text-slate-600 dark:text-slate-400" title={title}>
      <Icon name={icon} size={12} className="shrink-0 text-slate-400" />
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {trailing}
      <RowMenu label={`More actions for ${name}`} className="h-5 w-5 opacity-0 group-hover:opacity-100" items={[{ label: "Show DDL", onSelect: () => setShowing(true) }]} />
      {showing && <DDLViewer engine={engine} connectionId={connectionId} database={database} schemaName={schemaName} kind={kind} name={name} onClose={() => setShowing(false)} />}
    </li>
  );
}

function CountPill({ children }: { children: React.ReactNode }) {
  return <span className="min-w-[18px] rounded bg-slate-100 px-1 text-center text-[10.5px] leading-[17px] tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">{children}</span>;
}

// Every kind of object sits in a collapsible section, closed until it is
// opened, so a database with hundreds of tables stays readable. A search
// opens them, otherwise its matches would be hidden.
function ObjectGroup({ label, count, children, forceOpen = false }: { label: string; count: number | string; children: React.ReactNode; forceOpen?: boolean }) {
  const [opened, setOpened] = useState(false);
  const open = forceOpen || opened;
  const setOpen = (next: boolean | ((value: boolean) => boolean)) => setOpened(typeof next === "function" ? next(open) : next);
  return (
    <div className="mt-1">
      <button
        onClick={() => setOpen((o) => !o)}
        className="flex h-6 w-full items-center gap-2 rounded px-1 text-left text-[12px] text-slate-500 hover:text-slate-800 dark:text-slate-400 dark:hover:text-slate-200"
      >
        <Icon name={open ? "chevron-down" : "chevron-right"} size={12} className="-mr-1 text-slate-400" />
        {label}
        <CountPill>{count}</CountPill>
      </button>
      {open && <ul className="space-y-0.5">{children}</ul>}
    </div>
  );
}

function RoutineItem({ engine, schemaName, routine, connectionId, database }: { engine: string; schemaName: string; routine: RoutineInfo; connectionId: string; database?: string }) {
  const qualifiedName = qualifyName(engine, schemaName, routine.name);
  return (
    <ObjectRow
      icon={routine.kind === "procedure" ? "play" : "wand"}
      label={qualifiedName}
      title={`${routine.kind} ${qualifiedName}`}
      engine={engine}
      connectionId={connectionId}
      database={database}
      schemaName={schemaName}
      kind={routine.kind}
      name={routine.name}
    />
  );
}

function TriggerItem({ engine, schemaName, trigger, connectionId, database }: { engine: string; schemaName: string; trigger: TriggerInfo; connectionId: string; database?: string }) {
  const qualifiedTable = qualifyName(engine, schemaName, trigger.table);
  return (
    <ObjectRow
      icon="activity"
      label={trigger.name}
      title={trigger.table ? `${trigger.timing} ${trigger.event} ON ${qualifiedTable}` : `${trigger.timing} ${trigger.event}`.trim() || trigger.name}
      engine={engine}
      connectionId={connectionId}
      database={database}
      schemaName={schemaName}
      kind="trigger"
      name={trigger.name}
      trailing={trigger.table ? <span className="max-w-[45%] shrink-0 truncate text-right text-[10px] text-slate-400">{qualifiedTable}</span> : undefined}
    />
  );
}

function TableItem({ engine, schemaName, table, triggers = [], connectionId, database, readOnly, icon = "table" }: { engine: string; schemaName: string; table: TableInfo; triggers?: TriggerInfo[]; connectionId: string; database?: string; readOnly: boolean; icon?: IconName }) {
  const action = useContext(SchemaActions);
  const [copyState, setCopyState] = useState<"" | "copied" | "failed">("");
  const [exportState, setExportState] = useState<{ status: "" | "running" | "failed"; message?: string }>({ status: "" });
  const [showingDDL, setShowingDDL] = useState(false);
  const [open, setOpen] = useState(false);
  const [importing, setImporting] = useState(false);
  const qualifiedName = qualifyName(engine, schemaName, table.name);
  const quotedName = [schemaName, table.name].map(n => quoteIdentifier(engine, n)).join(".");
  // These engines have a query editor now, but no DDL viewer or export path
  // yet (both use the pooled SQL connection these engines don't have).
  const capabilities = useEngineCapabilities(engine);
  const hasObjectActions = capabilities.ddl || capabilities.csvExport || capabilities.jsonExport || capabilities.sqlExport || capabilities.cqlExport || capabilities.csvImport && !readOnly;
  const cqlExportable = icon === "table" && capabilities.cqlExport && !table.columns.some((column) => column.dataType.trim().toLowerCase() === "counter");
  const copyName = () => {
    navigator.clipboard.writeText(quotedName)
      .then(() => setCopyState("copied"), () => setCopyState("failed"))
      .finally(() => window.setTimeout(() => setCopyState(""), 1500));
  };
  const download = (format: "csv" | "json" | "sql" | "cql") => {
    setExportState({ status: "running" });
    exportTable(connectionId, { database, schema: schemaName, table: table.name, format })
      .then((blob) => {
        const url = URL.createObjectURL(blob);
        const link = document.createElement("a");
        link.href = url;
        link.download = `${table.name}.${format}`;
        link.click();
        window.setTimeout(() => URL.revokeObjectURL(url), 1000);
        setExportState({ status: "" });
      })
      .catch((error: unknown) => setExportState({ status: "failed", message: schemaError(error) }));
  };
  return (
    <li>
      <div className="group flex h-6 items-center rounded-md text-[12.5px] text-slate-700 hover:bg-slate-50 dark:text-slate-300 dark:hover:bg-slate-900">
        <button onClick={() => setOpen((o) => !o)} className="flex min-w-0 flex-1 items-center gap-2 px-1 text-left">
          <Icon name={open ? "chevron-down" : "chevron-right"} size={12} className="-mr-0.5 shrink-0 text-slate-400" />
          <Icon name={icon} size={13} className="shrink-0 text-slate-400 group-hover:text-slate-500" />
          <span className="truncate" title={qualifiedName}>{qualifiedName}</span>
          {triggers.length > 0 && (
            <span className="flex shrink-0 items-center gap-0.5 text-[10px] text-slate-400" title={`${triggers.length} trigger${triggers.length === 1 ? "" : "s"}: ${triggers.map((trigger) => trigger.name).join(", ")}`}>
              <Icon name="activity" size={11} />
              {triggers.length > 1 && triggers.length}
            </span>
          )}
        </button>
        {exportState.status === "running" && <span className="shrink-0 pr-1 text-[11px] text-slate-400">Exporting…</span>}
        {exportState.status === "failed" && <button type="button" onClick={() => setExportState({ status: "" })} title={exportState.message} className="shrink-0 pr-1 text-[11px] text-rose-500">Export failed</button>}
        <span className={`shrink-0 items-center gap-0.5 pr-0.5 ${copyState ? "flex" : "hidden group-hover:flex group-focus-within:flex"}`}>
          <RowAction icon="sql" title={engine === "mongodb" ? "Open find query in a new tab" : engine === "redis" || engine === "valkey" ? "Open a key scan in a new tab" : engine === "elasticsearch" ? "Open a search in a new tab" : "Open SELECT in a new tab (does not run it)"} onClick={() => action({ connectionId, database, sql: tableSelect(engine, schemaName, table.name, table.columns.map(c => c.name)) })} />
          <RowAction icon={copyState === "copied" ? "check" : "copy"} title={copyState === "failed" ? "Clipboard unavailable" : copyState === "copied" ? "Copied" : `Copy name: ${quotedName}`} onClick={copyName} tone={copyState === "failed" ? "text-rose-500" : copyState === "copied" ? "text-emerald-600" : undefined} />
          {hasObjectActions && <RowMenu
            label={`More actions for ${qualifiedName}`}
            className="h-5 w-5"
            items={engine === "mongodb" ? [{ label: "Find documents", onSelect: () => action({ connectionId, database, sql: tableSelect(engine, schemaName, table.name) }) }] : [
              ...(capabilities.ddl ? [{ label: "Show DDL", onSelect: () => setShowingDDL(true) }] : []),
              ...(capabilities.csvExport ? [{ label: "Export as CSV", onSelect: () => download("csv"), disabled: exportState.status === "running" }] : []),
              ...(capabilities.jsonExport ? [{ label: "Export as JSON", onSelect: () => download("json"), disabled: exportState.status === "running" }] : []),
              ...(capabilities.sqlExport ? [{ label: "Export as SQL (INSERT)", onSelect: () => download("sql"), disabled: exportState.status === "running" }] : []),
              ...(cqlExportable ? [{ label: "Export as CQL (INSERT JSON)", onSelect: () => download("cql"), disabled: exportState.status === "running" }] : []),
              ...(icon === "table" && capabilities.csvImport && !readOnly ? [{ label: "Import CSV…", onSelect: () => setImporting(true) }] : []),
            ]}
          />}
        </span>
      </div>
      {showingDDL && <DDLViewer engine={engine} connectionId={connectionId} database={database} schemaName={schemaName} kind={icon === "table" ? "table" : "view"} name={table.name} onClose={() => setShowingDDL(false)} />}
      {importing && <CsvImportDialog connectionId={connectionId} database={database} schemaName={schemaName} table={table} engine={engine} onClose={() => setImporting(false)} />}
      {open && engine !== "mongodb" && (
        <ul className="ml-[11px] border-l border-slate-200/80 pl-2.5 text-xs text-slate-500 dark:border-slate-800 dark:text-slate-400">
          {(table.columns ?? []).map((c) => (
            <li key={c.name} className="flex items-center gap-1.5 py-0.5" title={colTitle(c) + " · Double-click to add column to SQL"} onDoubleClick={() => action({ connectionId, database, sql: quoteIdentifier(engine, c.name), append: true })}>
              {c.pk ? (
                <Icon name="key" size={12} className="shrink-0 text-amber-500" />
              ) : c.references ? (
                <Icon name="key" size={12} className="shrink-0 text-sky-500" />
              ) : (
                <Icon name="columns" size={12} className="shrink-0 text-slate-300 dark:text-slate-600" />
              )}
              <span className={`min-w-0 flex-1 truncate ${c.pk ? "font-medium text-slate-700 dark:text-slate-200" : ""}`}>{c.name}</span>
              {c.pk && <span className="shrink-0 rounded bg-amber-50 px-1 text-[9px] font-medium text-amber-600 dark:bg-amber-500/10 dark:text-amber-400">PK</span>}
              {c.references && <span className="shrink-0 rounded bg-sky-50 px-1 text-[9px] font-medium text-sky-600 dark:bg-sky-500/10 dark:text-sky-400">FK</span>}
              <span className="ml-1 max-w-[38%] shrink-0 truncate text-right text-[10px] tracking-wide text-slate-400">{shortType(c.dataType)}</span>
            </li>
          ))}
          {table.indexes && table.indexes.filter((i) => !i.primary && (i.columns?.length ?? 0) > 0).length > 0 && (
            <li className="mt-1 space-y-0.5 border-t border-slate-100 pt-1 dark:border-slate-800/60">
              <div className="flex items-center gap-1.5 text-[10px] font-medium text-slate-400">
                <Icon name="filter" size={11} className="text-slate-300 dark:text-slate-600" />
                Indexes
              </div>
              {table.indexes.filter((i) => !i.primary && (i.columns?.length ?? 0) > 0).map((ix) => (
                <div key={ix.name} className="flex items-center gap-1.5 pl-3.5 py-0.5" title={`${ix.name} (${(ix.columns ?? []).join(", ")})`}>
                  <span className="truncate text-slate-500 dark:text-slate-400">{(ix.columns ?? []).join(", ")}</span>
                  {ix.unique && <span className="shrink-0 rounded bg-slate-100 px-1 text-[9px] font-medium text-slate-500 dark:bg-slate-800 dark:text-slate-400">Uniq</span>}
                </div>
              ))}
            </li>
          )}
          {triggers.length > 0 && (
            <li className="mt-1 space-y-0.5 border-t border-slate-100 pt-1 dark:border-slate-800/60">
              <div className="flex items-center gap-1.5 text-[10px] font-medium text-slate-400">
                <Icon name="activity" size={11} className="text-slate-300 dark:text-slate-600" />
                Triggers
              </div>
              {triggers.map((trigger) => (
                <TableTrigger key={trigger.name} engine={engine} schemaName={schemaName} trigger={trigger} connectionId={connectionId} database={database} />
              ))}
            </li>
          )}
        </ul>
      )}
    </li>
  );
}

// A trigger inside its table: when it fires, and its definition on request.
function TableTrigger({ engine, schemaName, trigger, connectionId, database }: { engine: string; schemaName: string; trigger: TriggerInfo; connectionId: string; database?: string }) {
  const [showing, setShowing] = useState(false);
  const when = `${trigger.timing} ${trigger.event}`.trim().toLowerCase();
  return (
    <div className="group flex items-center gap-1.5 py-0.5 pl-3.5" title={`${trigger.name}: ${when}`}>
      <span className="min-w-0 flex-1 truncate text-slate-600 dark:text-slate-300">{trigger.name}</span>
      {when && <span className="shrink-0 text-[10px] text-slate-400">{when}</span>}
      <RowMenu label={`More actions for ${trigger.name}`} className="h-5 w-5 opacity-0 group-hover:opacity-100" items={[{ label: "Show DDL", onSelect: () => setShowing(true) }]} />
      {showing && <DDLViewer engine={engine} connectionId={connectionId} database={database} schemaName={schemaName} kind="trigger" name={trigger.name} onClose={() => setShowing(false)} />}
    </div>
  );
}

function RowAction({ icon, title, onClick, tone }: { icon: IconName; title: string; onClick: () => void; tone?: string }) {
  return (
    <button type="button" title={title} aria-label={title} onClick={onClick} className={`grid h-5 w-5 place-items-center rounded hover:bg-slate-200 dark:hover:bg-slate-800 ${tone ?? "text-slate-400 hover:text-slate-700 dark:hover:text-slate-200"}`}>
      <Icon name={icon} size={12} />
    </button>
  );
}

function Hint({ children }: { children: React.ReactNode }) {
  return <p className="px-1 text-xs text-slate-500">{children}</p>;
}

function schemaError(error: unknown) {
  if (error instanceof ApiError) return error.body.message;
  return error instanceof Error ? error.message : "unknown metadata error";
}
