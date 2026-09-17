import { useMemo, useState } from "react";
import { Modal } from "../../components/ui";
import { EnvBadge, envKind } from "../../components/EnvBadge";
import { engineLabel } from "../../components/EngineLogo";
import type { Connection } from "../connections/api";
import { scriptChanges, statementEffect, type MultiRunTarget } from "./multiRun";
import { splitStatements } from "./sqlText";

// Picks the connections a script runs on. Reads run straight away; a script
// that changes anything names what it will change and where, and waits for an
// explicit confirmation.
export default function MultiRunDialog({
  sql,
  source,
  connections,
  initialIds,
  onClose,
  onRun,
}: {
  sql: string;
  /** What the SQL is: the selection or the whole editor. */
  source: "selection" | "editor";
  connections: Connection[];
  initialIds: string[];
  onClose: () => void;
  onRun: (targets: MultiRunTarget[], statements: string[], concurrency: number) => void;
}) {
  const usableConnections = useMemo(() => connections.filter((connection) => !["mongodb", "redis", "valkey", "elasticsearch"].includes(connection.engine)), [connections]);
  const [selected, setSelected] = useState(() => new Set(initialIds.filter((id) => usableConnections.some((connection) => connection.id === id))));
  const [databases, setDatabases] = useState<Record<string, string>>({});
  const [confirmed, setConfirmed] = useState(false);
  const [filter, setFilter] = useState("");
  const [concurrency, setConcurrency] = useState(() => {
    const saved = Number(localStorage.getItem(CONCURRENCY_KEY));
    return Number.isInteger(saved) && saved >= 1 && saved <= MAX_CONCURRENCY ? saved : 4;
  });

  const sharedEngine = useMemo(() => {
    const engines = new Set(usableConnections.filter((connection) => selected.has(connection.id)).map((connection) => connection.engine.toLowerCase()));
    return engines.size === 1 ? [...engines][0] : undefined;
  }, [usableConnections, selected]);
  const statements = useMemo(() => splitStatements(sql, sharedEngine).map((statement) => statement.sql.trim()).filter(Boolean), [sql, sharedEngine]);
  const changes = scriptChanges(statements);
  const changing = statements.filter((statement) => statementEffect(statement) === "change").length;
  const chosen = usableConnections.filter((connection) => selected.has(connection.id));
  const production = chosen.filter((connection) => envKind(connection.environment) === "prod");
  const visible = usableConnections.filter((connection) => `${connection.name} ${connection.engine} ${connection.environment} ${connection.database}`.toLowerCase().includes(filter.trim().toLowerCase()));
  // Confirming covers the connections chosen at that moment; changing the
  // choice asks again.
  const ready = chosen.length > 0 && statements.length > 0 && (!changes || confirmed);

  function toggle(id: string) {
    setConfirmed(false);
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  function setAll(ids: string[]) {
    setConfirmed(false);
    setSelected(new Set(ids));
  }

  function run() {
    if (!ready) return;
    onRun(
      chosen.map((connection) => ({
        connectionId: connection.id,
        name: connection.name,
        engine: connection.engine,
        environment: connection.environment,
        database: (databases[connection.id] ?? connection.database).trim(),
      })),
      statements,
      concurrency,
    );
  }

  return (
    <Modal title="Run on several connections" onClose={onClose} size="lg">
      <div className="space-y-3 text-[13px]">
        <p className="text-slate-600 dark:text-slate-300">
          {statements.length === 1 ? "One statement" : `${statements.length} statements`} from {source === "selection" ? "the selection" : "the editor"}
          {changes ? `, ${changing} of them changing data or schema.` : ", reading only."} Each connection runs them in order in auto-commit and stops at its
          first error; policies, row backups and history apply to each one as usual. Rowset runs them, so this page stays usable meanwhile.
        </p>

        <div className="flex items-center gap-2">
          <input
            value={filter}
            onChange={(event) => setFilter(event.target.value)}
            placeholder="Filter connections"
            aria-label="Filter connections"
            className="h-8 min-w-0 flex-1 rounded-md border border-slate-200 bg-white px-2.5 text-[12.5px] outline-none placeholder:text-slate-400 focus:border-slate-400 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-200"
          />
          <button type="button" onClick={() => setAll([...selected, ...visible.map((connection) => connection.id)])} className={linkButton}>Select shown</button>
          <button type="button" onClick={() => setAll([])} className={linkButton}>Clear</button>
        </div>

        <div className="max-h-72 overflow-auto rounded-md border border-slate-200 dark:border-slate-800">
          {visible.length === 0 && <p className="p-3 text-slate-500">No connection matches.</p>}
          {visible.map((connection) => (
            <label key={connection.id} className="flex items-center gap-2 border-b border-slate-100 px-2.5 py-1.5 last:border-b-0 hover:bg-slate-50 dark:border-slate-900 dark:hover:bg-slate-900">
              <input type="checkbox" checked={selected.has(connection.id)} onChange={() => toggle(connection.id)} aria-label={`Run on ${connection.name}`} />
              <span className="min-w-0 flex-1 truncate font-medium text-slate-800 dark:text-slate-100">{connection.name}</span>
              <span className="shrink-0 text-[11px] text-slate-400">{engineLabel(connection.engine)}</span>
              <EnvBadge env={connection.environment} />
              <input
                value={databases[connection.id] ?? connection.database}
                onChange={(event) => { setConfirmed(false); setDatabases((current) => ({ ...current, [connection.id]: event.target.value })); }}
                aria-label={`Database on ${connection.name}`}
                title="Database to run in"
                className="h-7 w-36 shrink-0 rounded border border-slate-200 bg-white px-2 font-mono text-[11.5px] text-slate-700 outline-none focus:border-slate-400 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-200"
              />
            </label>
          ))}
        </div>

        {changes && chosen.length > 0 && (
          <label className={`flex items-start gap-2 rounded-md border p-2.5 ${production.length ? "border-rose-300 bg-rose-50 text-rose-800 dark:border-rose-500/40 dark:bg-rose-500/10 dark:text-rose-200" : "border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-500/40 dark:bg-amber-500/10 dark:text-amber-200"}`}>
            <input type="checkbox" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} className="mt-0.5" />
            <span>
              Change data or schema on {chosen.length === 1 ? "1 connection" : `${chosen.length} connections`}
              {production.length > 0 && <>, including production: <strong>{production.map((connection) => connection.name).join(", ")}</strong></>}. Each statement is
              committed as soon as it succeeds.
            </span>
          </label>
        )}

        <div className="flex items-center justify-end gap-2 pt-1">
          <label className="mr-auto flex items-center gap-2 text-[12px] text-slate-600 dark:text-slate-300" title="Connections that run at the same time; the rest wait their turn">
            At once
            <select
              value={concurrency}
              onChange={(event) => { const value = Number(event.target.value); setConcurrency(value); localStorage.setItem(CONCURRENCY_KEY, String(value)); }}
              aria-label="Connections at once"
              className="h-7 rounded-md border border-slate-200 bg-white px-1.5 text-[12px] text-slate-700 outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-slate-200"
            >
              {Array.from({ length: MAX_CONCURRENCY }, (_, index) => index + 1).map((value) => <option key={value} value={value}>{value}</option>)}
            </select>
          </label>
          <button type="button" onClick={onClose} className="h-8 rounded-md border border-slate-200 px-3 text-[13px] text-slate-600 hover:bg-slate-50 dark:border-slate-800 dark:text-slate-300 dark:hover:bg-slate-900">Cancel</button>
          <button
            type="button"
            onClick={run}
            disabled={!ready}
            className="h-8 rounded-md bg-emerald-700 px-3 text-[13px] font-medium text-white hover:bg-emerald-600 disabled:cursor-not-allowed disabled:opacity-50 dark:bg-emerald-600 dark:hover:bg-emerald-500"
          >
            {chosen.length === 1 ? "Run on 1 connection" : `Run on ${chosen.length} connections`}
          </button>
        </div>
      </div>
    </Modal>
  );
}

const CONCURRENCY_KEY = "rowset.multirun.concurrency";
const MAX_CONCURRENCY = 10;

const linkButton = "h-8 shrink-0 rounded-md px-2 text-[12px] text-slate-500 hover:bg-slate-100 hover:text-slate-800 dark:hover:bg-slate-900 dark:hover:text-slate-200";
