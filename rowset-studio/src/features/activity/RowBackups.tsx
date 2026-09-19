import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router";
import { api } from "../../lib/api";
import { useAuth } from "../../lib/auth";
import { useShared } from "../../lib/instance";
import { Button, Modal, Panel } from "../../components/ui";
import { Icon } from "../../components/Icon";
import EngineLogo from "../../components/EngineLogo";
import type { Connection } from "../connections/api";

export interface RowBackup {
  id: string;
  connectionId: string;
  database: string;
  schema: string;
  table: string;
  kind: "update" | "delete";
  rows: number;
  statement: string;
  createdAt: string;
}

export function listRowBackups() {
  return api<{ backups: RowBackup[] }>("/row-backups").then((r) => r.backups);
}

function restoreScript(id: string) {
  return api<{ sql: string; connectionId: string; database: string; table: string }>(`/row-backups/${id}/restore`);
}

function applyRowBackup(id: string) {
  return api<{ rows: number }>(`/row-backups/${id}/apply`, { method: "POST" });
}

function deleteRowBackup(id: string) {
  return api<void>(`/row-backups/${id}`, { method: "DELETE" });
}

// Rows saved before an UPDATE or DELETE, each with a script that puts them back.
export default function RowBackups({ connections, search }: { connections: Connection[]; search: string }) {
  // On a shared server a backup script shows stored values, so only
  // administrators open it; everyone can restore.
  const shared = useShared();
  const role = useAuth((s) => s.user?.role);
  const canOpenScript = !shared || role === "admin";
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [removing, setRemoving] = useState<RowBackup | null>(null);
  const [restoring, setRestoring] = useState<RowBackup | null>(null);
  const [restored, setRestored] = useState("");
  const [opening, setOpening] = useState("");
  const [error, setError] = useState("");
  const backups = useQuery({ queryKey: ["row-backups"], queryFn: listRowBackups });
  const remove = useMutation({
    mutationFn: (id: string) => deleteRowBackup(id),
    onSuccess: () => { setRemoving(null); void queryClient.invalidateQueries({ queryKey: ["row-backups"] }); },
  });
  const apply = useMutation({
    mutationFn: (item: RowBackup) => applyRowBackup(item.id),
    onSuccess: (result, item) => { setRestoring(null); setRestored(`${result.rows} row(s) of ${item.table} restored.`); },
    onError: (err) => { setRestoring(null); setError(err instanceof Error ? err.message : "The restore failed; nothing was changed."); },
  });
  const byId = new Map(connections.map((connection) => [connection.id, connection]));
  const needle = search.trim().toLowerCase();
  const rows = (backups.data ?? []).filter((item) => !needle || item.statement.toLowerCase().includes(needle) || item.table.toLowerCase().includes(needle));

  async function openRestore(item: RowBackup) {
    setOpening(item.id);
    setError("");
    try {
      const script = await restoreScript(item.id);
      navigate("/editor", { state: { openSql: script.sql, connectionId: script.connectionId, database: script.database || undefined, title: `Restore ${script.table}`, restoreOf: item.id } });
    } catch (err) {
      setError(err instanceof Error ? err.message : "The restore script could not be built.");
    } finally {
      setOpening("");
    }
  }

  return (
    <Panel className="overflow-hidden">
      {restored && <p role="status" className="border-b border-emerald-200 bg-emerald-50 px-3 py-2 text-[12px] text-emerald-700 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-300">{restored}</p>}
      {error && <p role="alert" className="border-b border-rose-200 bg-rose-50 px-3 py-2 text-[12px] text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300">{error}</p>}
      {backups.isLoading ? (
        <p className="p-6 text-sm text-slate-500">Loading backups…</p>
      ) : backups.isError ? (
        <p className="p-6 text-sm text-rose-600">Row backups are unavailable. <button className="underline" onClick={() => void backups.refetch()}>Retry</button></p>
      ) : rows.length === 0 ? (
        <div className="p-6 text-sm text-slate-500">
          <p className="font-medium text-slate-700 dark:text-slate-200">{backups.data?.length ? "No backups match this search." : "No row backups yet"}</p>
          {!backups.data?.length && <p className="mt-1 max-w-xl">Before an UPDATE or DELETE on one table with a WHERE clause (SQL or CQL), or a MongoDB document update/delete, Rowset saves what it is about to change (up to 10,000 rows or documents). Open a backup's restore script here to put them back.</p>}
        </div>
      ) : (
        <div className="overflow-x-auto">
          <table className="min-w-full text-left text-[13px]">
            <thead className="border-b border-slate-200 bg-slate-50 text-[11px] text-slate-400 dark:border-slate-800 dark:bg-slate-900/40">
              <tr>
                <th className="px-3 py-2 font-medium">Time</th>
                <th className="px-3 py-2 font-medium">Connection</th>
                <th className="px-3 py-2 font-medium">Table</th>
                <th className="px-3 py-2 font-medium">Statement</th>
                <th className="px-3 py-2 text-right font-medium">Rows</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {rows.map((item) => {
                const connection = byId.get(item.connectionId);
                // MySQL names the database as the schema; show it once.
                const table = [item.database, item.schema === item.database ? "" : item.schema, item.table].filter(Boolean).join(".");
                return (
                  <tr key={item.id} className="group hover:bg-slate-50 dark:hover:bg-slate-900/40">
                    <td className="whitespace-nowrap px-3 py-2 text-[12px] text-slate-500" title={item.createdAt}>{new Date(item.createdAt).toLocaleString()}</td>
                    <td className="whitespace-nowrap px-3 py-2">
                      {connection ? <span className="inline-flex items-center gap-1.5"><EngineLogo engine={connection.engine} size={14} />{connection.name}</span> : <span className="text-slate-400">Removed connection</span>}
                    </td>
                    <td className="whitespace-nowrap px-3 py-2">
                      <span className={`mr-1.5 inline-block w-12 text-[11px] font-medium ${item.kind === "delete" ? "text-rose-600 dark:text-rose-400" : "text-amber-600 dark:text-amber-400"}`}>{item.kind === "delete" ? "DELETE" : "UPDATE"}</span>
                      <span className="font-mono text-[12px] text-slate-700 dark:text-slate-200">{table}</span>
                    </td>
                    <td className="max-w-[440px] px-3 py-2"><code className="block truncate font-mono text-[12px] text-slate-500" title={item.statement}>{item.statement}</code></td>
                    <td className="whitespace-nowrap px-3 py-2 text-right tabular-nums text-slate-500">{item.rows}</td>
                    <td className="whitespace-nowrap px-2 py-2">
                      <span className="flex justify-end gap-1">
                        <button type="button" disabled={!connection} onClick={() => { setError(""); setRestored(""); setRestoring(item); }} title={connection ? "Put these rows back now, in one transaction" : "The connection was removed"} className="inline-flex h-6 items-center gap-1 rounded border border-slate-200 bg-white px-2 text-[12px] font-medium text-slate-700 hover:bg-slate-50 disabled:opacity-40 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200 dark:hover:bg-slate-800">
                          <Icon name="history" size={12} />Restore
                        </button>
                        {canOpenScript && <button type="button" disabled={!connection || opening === item.id} onClick={() => void openRestore(item)} title={connection ? "Open the restore statements in a new editor tab to review or edit" : "The connection was removed"} className="inline-flex h-6 items-center gap-1 rounded px-2 text-[12px] text-slate-600 hover:bg-slate-200 hover:text-slate-900 disabled:opacity-40 dark:text-slate-300 dark:hover:bg-slate-800">
                          <Icon name="sql" size={12} />{opening === item.id ? "Opening…" : "Script"}
                        </button>}
                        <button type="button" onClick={() => setRemoving(item)} title="Delete backup" aria-label="Delete backup" className="grid h-6 w-6 place-items-center rounded text-slate-400 opacity-60 hover:bg-slate-200 hover:text-rose-600 group-hover:opacity-100 dark:hover:bg-slate-800">
                          <Icon name="close" size={12} />
                        </button>
                      </span>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {restoring && (
      <Modal onClose={() => setRestoring(null)} title="Restore these rows?">
        <p className="text-[13px] text-slate-600 dark:text-slate-300">
          {restoring.kind === "delete"
            ? `The ${restoring.rows} deleted row(s) are inserted back into ${restoring.table}.`
            : `The ${restoring.rows} row(s) of ${restoring.table} get the values they had before the UPDATE; changes made to them since then are overwritten.`}
        </p>
        <p className="mt-2 text-[13px] text-slate-600 dark:text-slate-300">
          {(() => {
            const engine = byId.get(restoring.connectionId)?.engine;
            if (engine === "mongodb") return "Documents are restored one at a time, not in a single transaction (MongoDB transactions need a replica set). A failure partway through leaves the ones already restored in place. Policies apply as in the editor.";
            if (engine === "cassandra") return "Rows are restored in CQL batches of up to 50 (each batch atomic; a failure partway through leaves earlier batches applied). Policies apply as in the editor.";
            if (engine === "redis" || engine === "valkey") return "The key is put back exactly as it was (or removed, if it did not exist before) in one operation. Policies apply as in the editor.";
            if (engine === "elasticsearch") return "The document is re-indexed exactly as it was, in one operation. Policies apply as in the editor.";
            return "It runs in one transaction: if any statement fails, nothing changes. Policies apply as in the editor.";
          })()}
        </p>
        <div className="mt-4 flex justify-end gap-2">
          <button type="button" onClick={() => setRestoring(null)} className="h-8 rounded-md px-3 text-[13px] text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800">Cancel</button>
          <Button disabled={apply.isPending} onClick={() => apply.mutate(restoring)}>{apply.isPending ? "Restoring…" : "Restore rows"}</Button>
        </div>
      </Modal>
      )}
      {removing && (
      <Modal onClose={() => setRemoving(null)} title="Delete row backup?">
        <p className="text-[13px] text-slate-600 dark:text-slate-300">The saved copy of {removing.rows} row(s) from {removing.table} is removed. The table itself does not change.</p>
        <div className="mt-4 flex justify-end gap-2">
          <button type="button" onClick={() => setRemoving(null)} className="h-8 rounded-md px-3 text-[13px] text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800">Cancel</button>
          <Button disabled={remove.isPending} onClick={() => remove.mutate(removing.id)}>Delete backup</Button>
        </div>
      </Modal>
      )}
    </Panel>
  );
}
