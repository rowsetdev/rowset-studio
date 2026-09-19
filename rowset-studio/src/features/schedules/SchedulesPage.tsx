import { useEffect, useMemo, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useLocation, useNavigate } from "react-router";
import { Button, Field, Input, PageHeader, Panel, Select, Textarea } from "../../components/ui";
import { Icon } from "../../components/Icon";
import { useConnections } from "../connections/useConnections";
import { createSchedule, deleteSchedule, downloadRunFile, listRuns, listSchedules, runSchedule, scheduleDefaults, updateSchedule, type ScheduledInput, type ScheduledQuery } from "./api";
import { browserTimeZone, describeSchedule, type ScheduleSpec } from "./scheduleText";

type Repeat = "daily" | "weekdays" | "days" | "interval";
const DAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

interface Draft extends ScheduledInput {
  id?: string;
  repeat: Repeat;
}

function repeatOf(spec: ScheduleSpec): Repeat {
  if (spec.kind === "interval") return "interval";
  if (spec.kind === "daily") return "daily";
  return [...(spec.days ?? [])].sort().join(",") === "1,2,3,4,5" ? "weekdays" : "days";
}

function specOf(draft: Draft): ScheduleSpec {
  const base = { timezone: draft.schedule.timezone };
  if (draft.repeat === "interval") return { ...base, kind: "interval", everyMinutes: draft.schedule.everyMinutes ?? 60 };
  if (draft.repeat === "daily") return { ...base, kind: "daily", time: draft.schedule.time ?? "10:00" };
  const days = draft.repeat === "weekdays" ? [1, 2, 3, 4, 5] : draft.schedule.days ?? [];
  return { ...base, kind: "weekly", time: draft.schedule.time ?? "10:00", days };
}

function emptyDraft(outputDir: string, connectionId = "", sql = "", database = ""): Draft {
  return { name: "", connectionId, database, sql, schedule: { kind: "daily", time: "10:00", days: [1, 2, 3, 4, 5], everyMinutes: 60, timezone: browserTimeZone() }, repeat: "daily", outputDir, format: "csv", catchUp: true, enabled: true };
}

function timeZones(): string[] {
  const intl = Intl as unknown as { supportedValuesOf?: (key: string) => string[] };
  const zones = intl.supportedValuesOf?.("timeZone") ?? [];
  const local = browserTimeZone();
  return zones.includes(local) ? zones : [local, ...zones];
}

function when(iso?: string | null) {
  return iso ? new Date(iso).toLocaleString() : "—";
}

// Scheduled queries run a SELECT at set times and save each result as a file.
export default function SchedulesPage() {
  const queryClient = useQueryClient();
  const location = useLocation();
  const navigate = useNavigate();
  const { data: connections = [] } = useConnections();
  const schedulableConnections = connections.filter((connection) => !["mongodb", "redis", "valkey", "cassandra", "elasticsearch"].includes(connection.engine));
  const defaults = useQuery({ queryKey: ["schedule-defaults"], queryFn: scheduleDefaults });
  const list = useQuery({
    queryKey: ["schedules"],
    queryFn: listSchedules,
    refetchInterval: (query) => (query.state.data?.some((item) => item.running) ? 3000 : 30000),
  });
  const schedules = useMemo(() => list.data ?? [], [list.data]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const selected = schedules.find((item) => item.id === selectedId);
  const zones = useMemo(timeZones, []);

  // The editor's "Schedule this query" opens a new schedule with its SQL.
  useEffect(() => {
    const state = location.state as { newSchedule?: { sql: string; connectionId: string | null; database: string } } | null;
    if (!state?.newSchedule || !defaults.data) return;
    const { sql, connectionId, database } = state.newSchedule;
    setSelectedId(null);
    setDraft(emptyDraft(defaults.data.outputDir, connectionId ?? "", sql, database));
    navigate(location.pathname, { replace: true, state: null });
  }, [location.state, location.pathname, defaults.data, navigate]);

  useEffect(() => {
    if (!selected) return;
    setDraft({ ...selected, repeat: repeatOf(selected.schedule), schedule: { days: [1, 2, 3, 4, 5], everyMinutes: 60, time: "10:00", ...selected.schedule } });
    setError("");
  }, [selected]);

  function patch(values: Partial<Draft>) {
    setDraft((current) => (current ? { ...current, ...values } : current));
  }
  function patchSpec(values: Partial<ScheduleSpec>) {
    setDraft((current) => (current ? { ...current, schedule: { ...current.schedule, ...values } } : current));
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    if (!draft) return;
    setBusy(true);
    setError("");
    const input: ScheduledInput = { name: draft.name, connectionId: draft.connectionId, database: draft.database, sql: draft.sql, schedule: specOf(draft), outputDir: draft.outputDir, format: draft.format, catchUp: draft.catchUp, enabled: draft.enabled };
    try {
      const saved = draft.id ? await updateSchedule(draft.id, input) : await createSchedule(input);
      await queryClient.invalidateQueries({ queryKey: ["schedules"] });
      setSelectedId(saved.id);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not save the schedule");
    } finally {
      setBusy(false);
    }
  }

  async function act(action: () => Promise<unknown>, after?: () => void) {
    setError("");
    try {
      await action();
      after?.();
      await queryClient.invalidateQueries({ queryKey: ["schedules"] });
      await queryClient.invalidateQueries({ queryKey: ["schedule-runs"] });
    } catch (err) {
      setError(err instanceof Error ? err.message : "The action failed");
    }
  }

  return (
    <div className="flex h-full min-h-0 flex-col gap-3">
      <PageHeader
        icon="clock"
        title="Schedules"
        subtitle="Run a SELECT at set times and save each result as a file. Schedules run while Rowset Studio is running, even with the browser closed."
        actions={
          <button type="button" disabled={!defaults.data || schedulableConnections.length === 0} onClick={() => { setSelectedId(null); setDraft(emptyDraft(defaults.data?.outputDir ?? "", schedulableConnections[0]?.id ?? "")); }} className="inline-flex h-8 items-center gap-1.5 rounded-md bg-brand-600 px-3 text-[12px] font-medium text-white hover:bg-brand-500 disabled:opacity-50">
            <Icon name="plus" size={13} />New schedule
          </button>
        }
      />
      <div className="grid min-h-0 flex-1 gap-3 lg:grid-cols-[280px_minmax(0,1fr)]">
        <Panel className="flex min-h-0 flex-col overflow-hidden">
          <ul className="min-h-0 flex-1 overflow-y-auto p-1">
            {list.isLoading && <li className="p-3 text-[12px] text-slate-500">Loading…</li>}
            {list.isError && <li className="p-3 text-[12px] text-rose-600">Schedules unavailable.</li>}
            {!list.isLoading && !schedules.length && <li className="p-3 text-[12px] text-slate-500">No schedules yet. Create one, or use “Schedule this query” in the SQL editor.</li>}
            {schedules.map((item) => (
              <li key={item.id}>
                <button type="button" onClick={() => setSelectedId(item.id)} className={`block w-full rounded px-2.5 py-2 text-left ${item.id === selectedId ? "bg-slate-100 dark:bg-slate-800" : "hover:bg-slate-50 dark:hover:bg-slate-900"}`}>
                  <span className="flex items-center gap-2">
                    <StatusDot item={item} />
                    <span className="truncate text-[13px] font-medium text-slate-800 dark:text-slate-100">{item.name}</span>
                  </span>
                  <span className="mt-0.5 block truncate text-[11px] text-slate-500">{item.enabled ? describeSchedule(item.schedule) : "Paused"}</span>
                </button>
              </li>
            ))}
          </ul>
        </Panel>

        <div className="min-h-0 space-y-3 overflow-y-auto">
          {!draft && <Panel className="p-6 text-center text-[13px] text-slate-500">Select a schedule or create a new one.</Panel>}
          {draft && (
            <Panel className="p-4">
              <form onSubmit={save} className="grid gap-3">
                <div className="grid gap-3 md:grid-cols-2">
                  <Field label="Name"><Input value={draft.name} onChange={(e) => patch({ name: e.target.value })} placeholder="Daily sales report" required /></Field>
                  <Field label="Connection">
                    <Select value={draft.connectionId} onChange={(e) => patch({ connectionId: e.target.value })} required>
                      <option value="">Choose…</option>
                      {schedulableConnections.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
                    </Select>
                  </Field>
                </div>
                <Field label="Database (optional)"><Input value={draft.database} onChange={(e) => patch({ database: e.target.value })} placeholder="The connection's default database" /></Field>
                <Field label="SELECT statement">
                  <Textarea className="min-h-[140px] font-mono text-[12px]" value={draft.sql} onChange={(e) => patch({ sql: e.target.value })} placeholder="SELECT order_date, SUM(total) FROM orders WHERE order_date >= CURRENT_DATE - 5 GROUP BY order_date" required />
                </Field>

                <div className="grid gap-3 md:grid-cols-3">
                  <Field label="Repeat">
                    <Select value={draft.repeat} onChange={(e) => patch({ repeat: e.target.value as Repeat })}>
                      <option value="daily">Every day</option>
                      <option value="weekdays">Weekdays</option>
                      <option value="days">On chosen days</option>
                      <option value="interval">Every few minutes</option>
                    </Select>
                  </Field>
                  {draft.repeat === "interval" ? (
                    <Field label="Every (minutes)"><Input type="number" min={5} max={10080} value={draft.schedule.everyMinutes ?? 60} onChange={(e) => patchSpec({ everyMinutes: Number(e.target.value) })} /></Field>
                  ) : (
                    <Field label="At"><Input type="time" value={draft.schedule.time ?? "10:00"} onChange={(e) => patchSpec({ time: e.target.value })} required /></Field>
                  )}
                  <Field label="Time zone">
                    <Select value={draft.schedule.timezone} onChange={(e) => patchSpec({ timezone: e.target.value })}>
                      {zones.map((zone) => <option key={zone} value={zone}>{zone}</option>)}
                    </Select>
                  </Field>
                </div>
                {draft.repeat === "days" && (
                  <div className="flex flex-wrap gap-1.5">
                    {DAYS.map((label, day) => {
                      const on = (draft.schedule.days ?? []).includes(day);
                      return (
                        <button key={label} type="button" onClick={() => patchSpec({ days: on ? (draft.schedule.days ?? []).filter((d) => d !== day) : [...(draft.schedule.days ?? []), day] })} className={`h-7 rounded-md border px-2.5 text-[12px] ${on ? "border-slate-700 bg-slate-700 text-white dark:border-slate-300 dark:bg-slate-300 dark:text-slate-900" : "border-slate-200 bg-white text-slate-600 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300"}`}>
                          {label}
                        </button>
                      );
                    })}
                  </div>
                )}

                <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_140px]">
                  {defaults.data?.serverFolder
                    ? <p className="text-[12px] text-slate-500 dark:text-slate-400">Results are kept on the server; download them from the runs below.</p>
                    : <Field label="Save results in folder"><Input value={draft.outputDir} onChange={(e) => patch({ outputDir: e.target.value })} className="font-mono text-[12px]" required /></Field>}
                  <Field label="Format">
                    <Select value={draft.format} onChange={(e) => patch({ format: e.target.value as "csv" | "json" })}>
                      <option value="csv">CSV</option>
                      <option value="json">JSON</option>
                    </Select>
                  </Field>
                </div>
                <p className="-mt-1 text-[11px] text-slate-500">Each run writes a new file named after the schedule and the run time, e.g. <code>daily-sales-report_2026-09-12_100000.{draft.format}</code>.</p>

                <div className="grid gap-3 md:grid-cols-2">
                  <Field label="If Rowset Studio was closed at run time">
                    <Select value={draft.catchUp ? "run" : "skip"} onChange={(e) => patch({ catchUp: e.target.value === "run" })}>
                      <option value="run">Run once when it opens</option>
                      <option value="skip">Skip that run</option>
                    </Select>
                  </Field>
                  <label className="flex items-center gap-2 self-end pb-2 text-[13px] text-slate-700 dark:text-slate-200">
                    <input type="checkbox" checked={draft.enabled} onChange={(e) => patch({ enabled: e.target.checked })} />
                    Enabled
                  </label>
                </div>

                {error && <p role="alert" className="text-[12px] text-rose-600 dark:text-rose-400">{error}</p>}
                <div className="flex flex-wrap items-center gap-2">
                  <Button type="submit" disabled={busy}>{busy ? "Saving…" : draft.id ? "Save" : "Create schedule"}</Button>
                  {selected && (
                    <>
                      <button type="button" disabled={selected.running} onClick={() => void act(() => runSchedule(selected.id))} className="inline-flex h-8 items-center gap-1.5 rounded-md border border-slate-200 px-3 text-[12px] text-slate-700 hover:bg-slate-50 disabled:opacity-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800">
                        <Icon name="play" size={13} />{selected.running ? "Running…" : "Run now"}
                      </button>
                      <button type="button" onClick={() => void act(() => deleteSchedule(selected.id), () => { setSelectedId(null); setDraft(null); })} className="inline-flex h-8 items-center gap-1.5 rounded-md border border-rose-200 px-3 text-[12px] text-rose-600 hover:bg-rose-50 dark:border-rose-500/30 dark:hover:bg-rose-500/10">
                        <Icon name="close" size={13} />Delete
                      </button>
                      <span className="ml-auto text-[12px] text-slate-500">Next run: {selected.enabled ? when(selected.nextRunAt) : "paused"}</span>
                    </>
                  )}
                </div>
              </form>
            </Panel>
          )}
          {selected && <Runs query={selected} />}
        </div>
      </div>
    </div>
  );
}

function StatusDot({ item }: { item: ScheduledQuery }) {
  const tone = item.running ? "bg-sky-500" : !item.lastRun ? "bg-slate-300 dark:bg-slate-600" : item.lastRun.status === "error" ? "bg-rose-500" : "bg-emerald-500";
  return <span className={`h-2 w-2 shrink-0 rounded-full ${tone}`} title={item.running ? "Running" : item.lastRun?.status ?? "Never run"} />;
}

function Runs({ query }: { query: ScheduledQuery }) {
  const serverFolder = Boolean(useQuery({ queryKey: ["schedule-defaults"], queryFn: scheduleDefaults }).data?.serverFolder);
  const runs = useQuery({ queryKey: ["schedule-runs", query.id, query.lastRun?.id, query.running], queryFn: () => listRuns(query.id) });
  return (
    <Panel className="overflow-hidden">
      <div className="border-b border-slate-200 px-4 py-2.5 text-[13px] font-semibold text-slate-800 dark:border-slate-800 dark:text-slate-100">Runs</div>
      {!runs.data?.length && <p className="p-4 text-[12px] text-slate-500">No runs yet.</p>}
      {!!runs.data?.length && (
        <div className="overflow-x-auto">
          <table className="w-full text-left text-[12px]">
            <thead className="bg-slate-50 text-slate-500 dark:bg-slate-900/50">
              <tr>
                <th className="px-3 py-2 font-medium">Started</th>
                <th className="px-3 py-2 font-medium">Status</th>
                <th className="px-3 py-2 font-medium">Rows</th>
                <th className="px-3 py-2 font-medium">File or error</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {runs.data.map((run) => (
                <tr key={run.id} className="text-slate-700 dark:text-slate-300">
                  <td className="whitespace-nowrap px-3 py-2">{when(run.startedAt)}{run.trigger === "manual" && <span className="ml-1.5 text-[10px] text-slate-400">manual</span>}</td>
                  <td className={`px-3 py-2 font-medium ${run.status === "error" ? "text-rose-600 dark:text-rose-400" : run.status === "running" ? "text-sky-600" : "text-emerald-600 dark:text-emerald-400"}`}>{run.status}</td>
                  <td className="px-3 py-2 tabular-nums">{run.status === "running" ? "—" : run.rows}</td>
                  <td className="max-w-[520px] px-3 py-2">
                    {run.outputPath && (serverFolder
                      ? <button type="button" onClick={() => void downloadRunFile(query.id, run)} className="inline-flex items-center gap-1 text-brand-700 hover:underline dark:text-brand-300"><Icon name="download" size={12} />Download</button>
                      : <PathCell path={run.outputPath} />)}
                    {run.error && <span className="text-rose-600 dark:text-rose-400">{run.error}</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Panel>
  );
}

function PathCell({ path }: { path: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <span className="flex items-center gap-2">
      <code className="truncate font-mono text-[11px]" title={path}>{path}</code>
      <button type="button" title="Copy path" onClick={() => { void navigator.clipboard.writeText(path).then(() => { setCopied(true); window.setTimeout(() => setCopied(false), 1200); }); }} className="shrink-0 text-slate-400 hover:text-slate-700 dark:hover:text-slate-200">
        <Icon name={copied ? "check" : "copy"} size={12} />
      </button>
    </span>
  );
}
