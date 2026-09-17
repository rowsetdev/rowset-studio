import { useEffect, useRef, useState, type Dispatch, type SetStateAction } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router";
import { Panel } from "../../components/ui";
import { Icon } from "../../components/Icon";
import { EnvBadge, envKind, envRail } from "../../components/EnvBadge";
import RowMenu from "../../components/RowMenu";
import EngineLogo, { engineLabel } from "../../components/EngineLogo";
import SchemaBrowser from "./SchemaBrowser";
import { forgetSchema, listDatabases } from "./api";
import { useSchema } from "./useEditor";

const EXPLORER_TREE_KEY = "rowset.editor.explorerTree";

type TreeProps = {
  treeState: Record<string, boolean>;
  onTreeStateChange: Dispatch<SetStateAction<Record<string, boolean>>>;
};

const treeGuide = "ml-[13px] border-l border-slate-200/80 pl-1 dark:border-slate-800";
const shortcutLabel = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform) ? "⌘K" : "Ctrl K";

function CountPill({ children }: { children: React.ReactNode }) {
  return <span className="ml-auto min-w-[18px] shrink-0 rounded bg-slate-100 px-1 text-center text-[10.5px] leading-[17px] tabular-nums text-slate-500 dark:bg-slate-800 dark:text-slate-400">{children}</span>;
}

function TreeChevron({ open, onClick }: { open: boolean; onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} aria-label={open ? "Collapse" : "Expand"} className="grid h-5 w-4 shrink-0 place-items-center text-slate-400 hover:text-slate-700 dark:hover:text-slate-200">
      <Icon name={open ? "chevron-down" : "chevron-right"} size={12} />
    </button>
  );
}

export default function ExplorerPanel({
  connections,
  activeConnectionId,
  selectedDb,
  onSelectConnection,
  onSelectDatabase,
  onCollapse,
}: {
  connections: ExplorerConnection[];
  activeConnectionId: string | null;
  selectedDb: string;
  onSelectConnection: (id: string | null) => void;
  onSelectDatabase: (db: string) => void;
  onCollapse: () => void;
}) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  // Tree expansion belongs to the explorer, so expanding a database does not
  // re-render the SQL editor or change an in-flight query's closures.
  const [treeState, onTreeStateChange] = useState<Record<string, boolean>>(() => loadBooleanRecord(EXPLORER_TREE_KEY));
  useEffect(() => { localStorage.setItem(EXPLORER_TREE_KEY, JSON.stringify(treeState)); }, [treeState]);
  const [search, setSearch] = useState("");
  const searchInput = useRef<HTMLInputElement>(null);
  useEffect(() => {
    const focus = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && !event.shiftKey && !event.altKey && event.key.toLowerCase() === "k") {
        event.preventDefault();
        event.stopPropagation();
        searchInput.current?.focus();
        searchInput.current?.select();
      }
    };
    window.addEventListener("keydown", focus, true);
    return () => window.removeEventListener("keydown", focus, true);
  }, []);
  const needle = search.trim().toLowerCase();
  // A connection stays when its name or a database name matches, or when one of
  // its databases is expanded, since the table filter then applies inside it.
  const visible = needle ? connections.filter((conn) =>
    conn.name.toLowerCase().includes(needle) ||
    (queryClient.getQueryData<string[]>(["databases", conn.id]) ?? [conn.database]).some((db) => db.toLowerCase().includes(needle)) ||
    Object.entries(treeState).some(([key, open]) => open && key.startsWith(`database:${conn.id}:`))) : connections;

  return (
    <Panel className="flex min-h-0 flex-col overflow-hidden">
      <div className="flex h-9 shrink-0 items-center gap-2 px-2.5">
        <Icon name="database" size={15} className="text-slate-400" />
        <span className="truncate text-[13px] font-semibold text-slate-800 dark:text-slate-100">Database Explorer</span>
        <span className="ml-auto flex items-center gap-1">
          <button type="button" onClick={() => navigate("/connections")} title="Add a connection" aria-label="Add a connection" className="grid h-6 w-6 place-items-center rounded-md border border-slate-200 text-slate-500 hover:bg-slate-50 hover:text-slate-800 dark:border-slate-800 dark:hover:bg-slate-900 dark:hover:text-slate-100">
            <Icon name="plus" size={14} />
          </button>
          <button type="button" onClick={onCollapse} title="Collapse explorer" aria-label="Collapse explorer" className="grid h-6 w-6 place-items-center rounded-md text-slate-400 hover:bg-slate-100 hover:text-slate-800 dark:hover:bg-slate-900 dark:hover:text-slate-100">
            <Icon name="chevron-left" size={14} />
          </button>
        </span>
      </div>
      <div className="shrink-0 px-2 pb-2">
        <div className="relative">
          <Icon name="search" size={13} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-slate-400" />
          <input
            ref={searchInput}
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            onKeyDown={(event) => { if (event.key === "Escape") setSearch(""); }}
            placeholder="Search databases, tables, and columns…"
            aria-label="Search databases, tables and columns"
            className="h-7 w-full rounded-md border border-slate-200 bg-white pl-8 pr-12 text-[12px] text-slate-700 outline-none placeholder:text-slate-400 focus:border-slate-400 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-200"
          />
          {search ? (
            <button type="button" onClick={() => setSearch("")} aria-label="Clear search" className="absolute right-1.5 top-1/2 grid h-5 w-5 -translate-y-1/2 place-items-center rounded text-slate-400 hover:text-slate-700 dark:hover:text-slate-200">
              <Icon name="close" size={12} />
            </button>
          ) : (
            <kbd className="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 rounded border border-slate-200 bg-slate-50 px-1 font-sans text-[10px] text-slate-400 dark:border-slate-800 dark:bg-slate-900">{shortcutLabel}</kbd>
          )}
        </div>
      </div>

      {/* macOS draws its scrollbar over the content while scrolling; the
          right padding keeps it off the column types aligned to that edge. */}
      <div className="min-h-0 flex-1 overflow-auto border-t border-slate-200 pl-1 pr-3 text-[12.5px] dark:border-slate-800">
        <div className="divide-y divide-slate-100 dark:divide-slate-800/70">
          {Object.entries(groupConnections(visible)).map(([engine, items]) => (
            <EngineBranch
              key={engine}
              engine={engine}
              connections={items}
              activeConnectionId={activeConnectionId}
              onSelectConnection={onSelectConnection}
              selectedDb={selectedDb}
              onSelectDatabase={onSelectDatabase}
              search={needle}
              treeState={treeState}
              onTreeStateChange={onTreeStateChange}
            />
          ))}
        </div>
        {connections.length === 0 && <div className="px-2 py-3 text-xs text-slate-500">No connections yet. Add one with +.</div>}
        {connections.length > 0 && visible.length === 0 && <div className="px-2 py-3 text-xs text-slate-500">No connection or database matches “{search.trim()}”. Expand a database to search its tables.</div>}
      </div>
    </Panel>
  );
}

function EngineBranch({
  engine,
  connections,
  activeConnectionId,
  onSelectConnection,
  selectedDb,
  onSelectDatabase,
  search,
  treeState,
  onTreeStateChange,
}: {
  engine: string;
  connections: ExplorerConnection[];
  activeConnectionId: string | null;
  onSelectConnection: (id: string | null) => void;
  selectedDb: string;
  onSelectDatabase: (db: string) => void;
  search: string;
} & TreeProps) {
  const treeKey = `engine:${engine}`;
  const open = Boolean(search) || treeValue(treeState, treeKey, true);
  return (
    <div className="py-0.5">
      <button
        type="button"
        onClick={() => onTreeStateChange((current) => ({ ...current, [treeKey]: !open }))}
        className="flex h-7 w-full items-center gap-2 rounded-md px-1 text-left text-slate-800 hover:bg-slate-50 dark:text-slate-100 dark:hover:bg-slate-900"
      >
        <span className="grid w-4 shrink-0 place-items-center text-slate-400"><Icon name={open ? "chevron-down" : "chevron-right"} size={12} /></span>
        <EngineLogo engine={engine} size={15} />
        <span className="truncate text-[12.5px] font-medium">{engineLabel(engine)}</span>
        <CountPill>{connections.length}</CountPill>
      </button>
      {open && (
        <div className={treeGuide}>
          {connections.map((conn) => (
            <ConnectionBranch
              key={conn.id}
              conn={conn}
              active={conn.id === activeConnectionId}
              selectedDb={conn.id === activeConnectionId ? selectedDb : ""}
              onSelect={() => onSelectConnection(conn.id)}
              onSelectDatabase={(db) => {
                onSelectConnection(conn.id);
                onSelectDatabase(db);
              }}
              search={search}
              treeState={treeState}
              onTreeStateChange={onTreeStateChange}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function ConnectionBranch({
  conn,
  active,
  selectedDb,
  onSelect,
  onSelectDatabase,
  search,
  treeState,
  onTreeStateChange,
}: {
  conn: ExplorerConnection;
  active: boolean;
  selectedDb: string;
  onSelect: () => void;
  onSelectDatabase: (db: string) => void;
  search: string;
} & TreeProps) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const treeKey = `connection:${conn.id}`;
  const open = Boolean(search) || treeValue(treeState, treeKey, true);
  const { data: databases = [] } = useQuery({
    queryKey: ["databases", conn.id],
    queryFn: () => listDatabases(conn.id),
    enabled: open,
  });
  const databaseNames = uniqueNames(databases.length ? databases : [conn.database || "default"]);
  const nameMatches = !search || conn.name.toLowerCase().includes(search);
  const shown = (db: string) => nameMatches || db.toLowerCase().includes(search) || treeValue(treeState, `database:${conn.id}:${db}`, false);
  const split = splitDatabases(conn.engine, databaseNames, conn.database || "default");
  const primary = split.primary.filter(shown);
  const system = split.system.filter(shown);
  const activeDb = selectedDb || conn.database || "default";
  return (
    <div>
      <div className={`group flex h-7 items-center gap-1.5 rounded-md px-1 ${active ? "bg-slate-100 dark:bg-slate-800/60" : "hover:bg-slate-50 dark:hover:bg-slate-900"}`}>
        <TreeChevron open={open} onClick={() => onTreeStateChange((current) => ({ ...current, [treeKey]: !open }))} />
        <button type="button" onClick={onSelect} className="flex min-w-0 flex-1 items-center gap-2 text-left">
          <span title={`${conn.environment} connection`} className={`h-2 w-2 shrink-0 rounded-full ${envRail[envKind(conn.environment)]}`} />
          <span className="truncate font-medium text-slate-800 dark:text-slate-100">{conn.name}</span>
        </button>
        {envKind(conn.environment) === "prod" && <EnvBadge env={conn.environment} />}
        <RowMenu
          label={`${conn.name} actions`}
          className="opacity-60 group-hover:opacity-100"
          items={[
            { label: "Use in this tab", onSelect },
            {
              label: "Refresh databases and schema",
              onSelect: () => {
                void queryClient.invalidateQueries({ queryKey: ["databases", conn.id] });
                void forgetSchema(conn.id).finally(() => queryClient.invalidateQueries({ queryKey: ["schema", conn.id] }));
              },
            },
            { label: "Edit connection", onSelect: () => navigate("/connections") },
          ]}
        />
      </div>
      {open && (
        <div className={`${treeGuide} mt-0.5 border-t-0`}>
          {system.length > 0 && (
            <SystemDatabaseBranch
              connectionId={conn.id}
              engine={conn.engine}
              databaseNames={system}
              activeDb={activeDb}
              active={active}
              forceOpen={Boolean(search) && !nameMatches}
              search={search}
              onSelectDatabase={onSelectDatabase}
              treeState={treeState}
              onTreeStateChange={onTreeStateChange}
            />
          )}
          {primary.map((db) => (
            <DatabaseBranch
              key={db}
              name={db}
              active={active && db === activeDb}
              connectionId={conn.id}
              engine={conn.engine}
              search={search}
              treeState={treeState}
              onTreeStateChange={onTreeStateChange}
              onSelect={() => onSelectDatabase(db)}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function DatabaseBranch({
  name,
  active,
  connectionId,
  engine,
  search,
  treeState,
  onTreeStateChange,
  onSelect,
}: {
  name: string;
  active: boolean;
  connectionId: string;
  engine: string;
  search: string;
  onSelect: () => void;
} & TreeProps) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const treeKey = `database:${connectionId}:${name}`;
  const open = treeValue(treeState, treeKey, false);
  const shouldLoadSchema = open || active;
  const { data: schema, isFetching } = useSchema(connectionId, name, shouldLoadSchema);
  const tableCount = schema?.schemas.reduce((count, item) => count + (item.tables?.length ?? 0), 0);
  return (
    <div>
      <div className={`group flex h-6 items-center gap-1.5 rounded-md px-1 ${active ? "text-slate-900 dark:text-slate-100" : "text-slate-700 hover:bg-slate-50 dark:text-slate-300 dark:hover:bg-slate-900"}`}>
        <TreeChevron open={open} onClick={() => onTreeStateChange((current) => ({ ...current, [treeKey]: !open }))} />
        <button type="button" onClick={onSelect} className="flex min-w-0 flex-1 items-center gap-2 text-left">
          <Icon name="database" size={13} className={`shrink-0 ${active ? "text-slate-600 dark:text-slate-300" : "text-slate-400"}`} />
          <span className={`truncate ${active ? "font-medium" : ""}`}>{name}</span>
        </button>
        <RowMenu
          label={`${name} actions`}
          className="opacity-0 group-hover:opacity-100"
          items={[
            ...(engine !== "mongodb" ? [{ label: "Compare schema", onSelect: () => navigate(`/schema-compare?connection=${encodeURIComponent(connectionId)}&database=${encodeURIComponent(name)}`) },
            { label: "Show diagram", onSelect: () => navigate(`/diagram?connection=${encodeURIComponent(connectionId)}&database=${encodeURIComponent(name)}`) }] : []),
            { label: "Refresh schema", onSelect: () => void forgetSchema(connectionId).finally(() => queryClient.invalidateQueries({ queryKey: ["schema", connectionId, name] })) },
          ]}
        />
        <CountPill>{schema ? tableCount ?? 0 : !shouldLoadSchema ? "—" : isFetching ? "…" : 0}</CountPill>
      </div>
      {open && (
        <div className={`${treeGuide} pb-1 pt-0.5`}>
          <SchemaBrowser connectionId={connectionId} database={name} engine={engine} search={search} compact />
        </div>
      )}
    </div>
  );
}

function SystemDatabaseBranch({
  connectionId,
  engine,
  databaseNames,
  activeDb,
  active,
  forceOpen,
  search,
  onSelectDatabase,
  treeState,
  onTreeStateChange,
}: {
  connectionId: string;
  engine: string;
  databaseNames: string[];
  activeDb: string;
  active: boolean;
  forceOpen: boolean;
  search: string;
  onSelectDatabase: (db: string) => void;
} & TreeProps) {
  const treeKey = `system-databases:${connectionId}`;
  const open = forceOpen || treeValue(treeState, treeKey, false);
  return (
    <div className="border-b border-slate-100 pb-0.5 dark:border-slate-800/70">
      <div className="flex h-6 items-center gap-1.5 rounded-md px-1 text-slate-600 hover:bg-slate-50 dark:text-slate-400 dark:hover:bg-slate-900">
        <TreeChevron open={open} onClick={() => onTreeStateChange((current) => ({ ...current, [treeKey]: !open }))} />
        <button type="button" onClick={() => onTreeStateChange((current) => ({ ...current, [treeKey]: !open }))} className="flex min-w-0 flex-1 items-center gap-2 text-left">
          <Icon name="database" size={13} className="shrink-0 text-slate-400" />
          <span className="truncate">system databases</span>
        </button>
        <CountPill>{databaseNames.length}</CountPill>
      </div>
      {open && (
        <div className={treeGuide}>
          {databaseNames.map((db) => (
            <DatabaseBranch
              key={db}
              name={db}
              active={active && db === activeDb}
              connectionId={connectionId}
              engine={engine}
              search={search}
              treeState={treeState}
              onTreeStateChange={onTreeStateChange}
              onSelect={() => onSelectDatabase(db)}
            />
          ))}
        </div>
      )}
    </div>
  );
}

type ExplorerConnection = { id: string; name: string; engine: string; environment: string; database: string };

function groupConnections(connections: ExplorerConnection[]) {
  return connections.reduce<Record<string, ExplorerConnection[]>>((acc, conn) => {
    const key = conn.engine || "database";
    acc[key] = acc[key] ?? [];
    acc[key].push(conn);
    return acc;
  }, {});
}

function loadBooleanRecord(key: string) {
  const raw = localStorage.getItem(key);
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw) as Record<string, unknown>;
    if (!parsed || typeof parsed !== "object") return {};
    return Object.fromEntries(
      Object.entries(parsed).filter((entry): entry is [string, boolean] => typeof entry[1] === "boolean"),
    );
  } catch {
    return {};
  }
}

function treeValue(tree: Record<string, boolean>, key: string, fallback: boolean) {
  return tree[key] ?? fallback;
}

function uniqueNames(names: string[]) {
  return Array.from(new Set(names.filter(Boolean)));
}

function splitDatabases(engine: string, names: string[], _primaryDatabase: string) {
  const primary = uniqueNames(names.filter((name) => !isSystemDatabase(engine, name)));
  const system = uniqueNames(names.filter((name) => isSystemDatabase(engine, name))).sort((a, b) =>
    a.localeCompare(b),
  );
  return { primary, system };
}

function isSystemDatabase(engine: string, database: string) {
  const name = database.toLowerCase();
  if (engine === "mysql" || engine === "mariadb") {
    return ["information_schema", "mysql", "performance_schema", "sys"].includes(name);
  }
  if (engine === "postgres") {
    return ["postgres", "template0", "template1", "cloudsqladmin", "rdsadmin", "azure_maintenance", "azure_sys"].includes(name);
  }
  if (engine === "sqlserver" || engine === "mssql") {
    return ["master", "model", "msdb", "tempdb"].includes(name);
  }
  return false;
}
