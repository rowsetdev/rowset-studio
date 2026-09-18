import { useNavigate } from "react-router";
import { type ComponentType, useEffect, useState } from "react";
import { Button, Input, PageHeader, Panel, Select } from "../../components/ui";
import { EnvBadge } from "../../components/EnvBadge";
import { Icon } from "../../components/Icon";
import EngineLogo, { engineLabel } from "../../components/EngineLogo";
import { useAuth } from "../../lib/auth";
import { useActiveExtensions } from "../../app/extensions";
import { Connection } from "./api";
import ConnectionForm from "./ConnectionForm";
import { mongoQuery } from "../editor/mongoQuery";
import { redisQuery } from "../editor/RedisQueryBar";
import { elasticsearchQuery } from "../editor/ElasticsearchQueryBar";

function defaultOpenSql(engine: string): string {
  if (engine === "mongodb") return mongoQuery();
  if (engine === "redis" || engine === "valkey") return redisQuery();
  if (engine === "elasticsearch") return elasticsearchQuery();
  return "-- Write a query here\n";
}
import { useConnections, useDeleteConnection, useTestConnection } from "./useConnections";

export default function ConnectionsPage() {
  const active = useActiveExtensions();
  const address = active.find((item) => item.connectionAddress)?.connectionAddress;
  const actions = active.flatMap((item) => item.connectionActions ?? []);
  const subtitle = active.find((item) => item.connectionsSubtitle)?.connectionsSubtitle ?? "Your saved database connections.";
  const { data: connections, isLoading, isError } = useConnections();
  const isAdmin = useAuth((s) => s.user?.role === "admin");
  const [showForm, setShowForm] = useState(false);
  const [engine, setEngine] = useState("all");
  const [environment, setEnvironment] = useState("all");
  const [search, setSearch] = useState("");
  // Bumped to tell every visible row to (re)test; rows react to it in an effect.
  const [testAllToken, setTestAllToken] = useState(0);
  const rows = (connections ?? []).filter((c) => {
    if (engine !== "all" && c.engine !== engine) return false;
    if (environment !== "all" && c.environment !== environment) return false;
    const haystack = `${c.name} ${c.alias} ${c.host} ${c.database} ${c.connectionUsername}`.toLowerCase();
    return haystack.includes(search.toLowerCase());
  });

  return (
    <div className="space-y-4">
      <PageHeader
        icon="plug"
        title="Connections"
        subtitle={subtitle}
        actions={isAdmin ? (
          <div className="flex items-center gap-2">
            {rows.length > 0 && <button type="button" onClick={() => setTestAllToken((t) => t + 1)} className="inline-flex h-8 items-center gap-1.5 rounded-md border border-slate-200 bg-white px-2.5 text-[12px] font-medium text-slate-600 transition hover:bg-slate-50 hover:text-slate-900 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800">
              <Icon name="check" size={13} /> Test all ({rows.length})
            </button>}
            <Button onClick={() => setShowForm(true)}>New connection</Button>
          </div>
        ) : undefined}
      />

      <Panel className="grid gap-2 p-2.5 md:grid-cols-[180px_160px_minmax(0,1fr)]">
        <Select value={engine} onChange={(e) => setEngine(e.target.value)}>
          <option value="all">All engines</option>
          <option value="postgres">PostgreSQL</option>
          <option value="mssql">SQL Server</option>
          <option value="mysql">MySQL</option>
          <option value="mariadb">MariaDB</option>
          <option value="sqlite">SQLite</option><option value="duckdb">DuckDB</option><option value="clickhouse">ClickHouse</option><option value="mongodb">MongoDB</option><option value="cockroachdb">CockroachDB</option><option value="redis">Redis</option><option value="valkey">Valkey</option><option value="cassandra">Cassandra</option><option value="elasticsearch">Elasticsearch</option>
        </Select>
        <Select value={environment} onChange={(e) => setEnvironment(e.target.value)}>
          <option value="all">All environments</option>
          <option value="prod">Prod</option>
          <option value="test">Test</option>
          <option value="dev">Dev</option>
        </Select>
        <Input placeholder="Search connection, host, database..." value={search} onChange={(e) => setSearch(e.target.value)} />
      </Panel>

      <Panel className="overflow-hidden">
        {isLoading && <p className="p-4 text-slate-500">Loading…</p>}
        {isError && <p className="p-4 text-rose-600 dark:text-rose-400">Failed to load connections.</p>}

        {connections && connections.length === 0 && (
          <p className="p-4 text-slate-500">No connections yet. Create one to get started.</p>
        )}
        {connections && connections.length > 0 && rows.length === 0 && (
          <p className="p-4 text-slate-500">No matching connections.</p>
        )}

        {rows.length > 0 && (
          <table className="w-full text-left text-[13px]">
            <thead className="border-b border-slate-200 bg-slate-50 text-xs text-slate-500 dark:border-slate-800 dark:bg-slate-900/50">
              <tr>
                <th className="px-3 py-2.5 font-medium">Name</th>
                <th className="px-3 py-2.5 font-medium">Engine</th>
                <th className="px-3 py-2.5 font-medium">{address?.title ?? "Database"}</th>
                <th className="px-3 py-2.5 font-medium">Environment</th>
                <th className="px-3 py-2.5 text-right font-medium">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {rows.map((c) => (
                <ConnectionRow key={c.id} conn={c} isAdmin={isAdmin} Address={address?.cell} actions={actions} testAllToken={testAllToken} />
              ))}
            </tbody>
          </table>
        )}
      </Panel>

      {showForm && <ConnectionForm onClose={() => setShowForm(false)} />}
    </div>
  );
}

function ConnectionRow({
  conn,
  isAdmin,
  Address,
  actions,
  testAllToken,
}: {
  conn: Connection;
  isAdmin: boolean;
  Address?: ComponentType<{ connection: Connection; isAdmin: boolean }>;
  actions: ComponentType<{ connection: Connection }>[];
  testAllToken: number;
}) {
  const navigate = useNavigate();
  const test = useTestConnection();
  const del = useDeleteConnection();
  const [editing, setEditing] = useState(false);

  useEffect(() => {
    if (testAllToken > 0) test.mutate(conn.id);
    // Runs once per bump of the shared token; test.mutate is stable across renders.
  }, [testAllToken]);

  return (
    <tr className="text-slate-700 hover:bg-slate-50 dark:text-slate-300 dark:hover:bg-slate-900/70">
      <td className="px-3 py-2.5">
        <div className="flex items-center gap-1.5">
          <span className="font-medium text-slate-900 dark:text-slate-100">{conn.name}</span>
          {conn.readOnly && (
            <span title="Read-only: writes are blocked" className="inline-flex items-center gap-0.5 rounded bg-slate-100 px-1 py-0.5 text-[10px] font-medium text-slate-500 dark:bg-slate-800 dark:text-slate-400">
              <Icon name="lock" size={10} /> Read-only
            </span>
          )}
        </div>
        <div className="font-mono text-[11px] text-slate-400">{conn.alias || conn.name}</div>
      </td>
      <td className="px-3 py-2.5">
        <span className="inline-flex items-center gap-1.5 text-slate-600 dark:text-slate-400">
          <EngineLogo engine={conn.engine} size={16} />
          {engineLabel(conn.engine)}
        </span>
      </td>
      <td className="px-3 py-2.5 font-mono text-[12px] text-slate-500 dark:text-slate-400">
        {Address ? <Address connection={conn} isAdmin={isAdmin} /> : <span>{conn.engine === "sqlite" || conn.engine === "duckdb" ? conn.database : `${conn.host}:${conn.port}/${conn.database}`}</span>}
      </td>
      <td className="px-3 py-2.5">
        <EnvBadge env={conn.environment} />
      </td>
      <td className="px-3 py-2.5">
        <div className="flex items-center justify-end gap-2">
          <button type="button" className="text-xs text-brand-600" onClick={() => navigate("/editor", { state: { openSql: defaultOpenSql(conn.engine), connectionId: conn.id, database: conn.database, title: conn.name } })}>Open</button>
          <TestStatus result={test.data} pending={test.isPending} />
          {actions.map((Action, index) => <Action key={index} connection={conn} />)}
          {isAdmin && (
            <>
              <button
                onClick={() => test.mutate(conn.id)}
                className="inline-flex h-7 items-center gap-1.5 rounded-md border border-slate-200 bg-white px-2.5 text-xs font-medium text-slate-600 transition hover:bg-slate-50 hover:text-slate-900 disabled:opacity-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800"
                disabled={test.isPending}
              >
                <Icon name="check" size={13} /> Test
              </button>
              <button
                onClick={() => setEditing(true)}
                className="inline-flex h-7 items-center gap-1.5 rounded-md border border-slate-200 bg-white px-2.5 text-xs font-medium text-slate-600 transition hover:bg-slate-50 hover:text-slate-900 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800"
              >
                <Icon name="pencil" size={13} /> Edit
              </button>
              <button
                onClick={() => del.mutate(conn.id)}
                className="inline-flex h-7 items-center gap-1.5 rounded-md border border-rose-200 bg-white px-2.5 text-xs font-medium text-rose-600 transition hover:bg-rose-50 disabled:opacity-50 dark:border-rose-500/30 dark:bg-slate-900 dark:text-rose-400 dark:hover:bg-rose-500/10"
                disabled={del.isPending}
              >
                <Icon name="close" size={13} /> Delete
              </button>
            </>
          )}
        </div>
        {editing && <ConnectionForm connection={conn} onClose={() => setEditing(false)} />}
      </td>
    </tr>
  );
}

function TestStatus({
  result,
  pending,
}: {
  result?: { ok: boolean; latencyMs: number; error?: string };
  pending: boolean;
}) {
  if (pending) return <span className="text-xs text-slate-500">Testing…</span>;
  if (!result) return null;
  return result.ok ? (
    <span className="inline-flex items-center gap-1.5 text-xs text-emerald-600 dark:text-emerald-400">
      <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" /> {result.latencyMs} ms
    </span>
  ) : (
    <span
      className="inline-flex items-center gap-1.5 text-xs text-rose-600 dark:text-rose-400"
      title={result.error}
    >
      <span className="h-1.5 w-1.5 rounded-full bg-rose-500" /> Unreachable
    </span>
  );
}
