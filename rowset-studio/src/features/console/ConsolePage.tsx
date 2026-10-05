import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router";
import { Icon } from "../../components/Icon";
import { EnvBadge } from "../../components/EnvBadge";
import { listConnections } from "../connections/api";
import { getSchema, listDatabases } from "../editor/api";
import { ApiError } from "../../lib/api";
import { rowBackupEnabled } from "../../lib/preferences";
import ConsoleOutput, { type ConsoleEntry } from "./ConsoleOutput";
import { engineSpec } from "./consoleEngines";
import type { ConsoleBlock } from "./consoleFormat";
import type { ConsoleSettings } from "./consoleMeta";
import { runLine, type RunContext } from "./consoleRun";
import { pending, promptLabel } from "./consoleStatement";

// A shell for every engine Rowset connects to, in the browser. It is a client
// over the same API the query editor uses rather than a terminal: no process
// is started and no shell reaches the database, so a statement typed here is
// governed, backed up and audited exactly as one run from the editor is.

const historyLimit = 200;

export default function ConsolePage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const connections = useQuery({ queryKey: ["connections"], queryFn: listConnections });
  const [connectionId, setConnectionId] = useState("");
  const connection = connections.data?.find(item => item.id === connectionId);
  const spec = connection ? engineSpec(connection.engine) : undefined;

  const [database, setDatabase] = useState("");
  const [settings, setSettings] = useState<ConsoleSettings>({ expanded: false, timing: false });
  const [entries, setEntries] = useState<ConsoleEntry[]>([]);
  const [lines, setLines] = useState<string[]>([]);
  const [draft, setDraft] = useState("");
  const [running, setRunning] = useState(false);
  const [history, setHistory] = useState<string[]>([]);
  const [historyAt, setHistoryAt] = useState<number | null>(null);
  const nextId = useRef(1);
  const abort = useRef<AbortController | null>(null);
  const bottom = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLTextAreaElement>(null);

  // The first connection is selected for you; typing a command is the point
  // of the page, and choosing from a list first is friction.
  useEffect(() => {
    if (!connectionId && connections.data?.length) setConnectionId(connections.data[0].id);
  }, [connections.data, connectionId]);

  useEffect(() => {
    setDatabase(connection?.database ?? "");
    setEntries([]);
    setLines([]);
  }, [connection?.id, connection?.database]);

  useEffect(() => {
    bottom.current?.scrollIntoView({ block: "end" });
  }, [entries, lines]);

  const print = useCallback((blocks: ConsoleBlock[], of: ConsoleSettings) => {
    if (!blocks.length) return;
    setEntries(current => [...current, { id: nextId.current++, blocks, expanded: of.expanded, timing: of.timing }]);
  }, []);

  // The schema answers \dt, \d and their equivalents. React Query caches it
  // per connection and database, so a hundred \dt in a row is one request.
  const schema = useCallback(
    () => queryClient.fetchQuery({ queryKey: ["schema", connectionId, database], queryFn: () => getSchema(connectionId, database), staleTime: 60_000 }),
    [queryClient, connectionId, database],
  );
  const databases = useCallback(
    () => queryClient.fetchQuery({ queryKey: ["databases", connectionId], queryFn: () => listDatabases(connectionId), staleTime: 60_000 }),
    [queryClient, connectionId],
  );

  // The continuation prompt is indented to the width of the first one, the
  // way psql lines up a statement that spans several lines.
  const label = spec ? promptLabel(spec, database) : "";
  const prompt = useMemo(() => {
    if (!spec) return "> ";
    return lines.length ? `${" ".repeat(label.length)}-> ` : `${label}=> `;
  }, [spec, label, lines.length]);

  const submit = useCallback(async () => {
    if (!spec || !connection || running) return;
    const buffered = [...lines, draft];
    const ready = pending(spec, buffered);
    setDraft("");
    setHistoryAt(null);
    if (!ready) {
      setLines(buffered);
      return;
    }
    setLines([]);
    setHistory(current => [...current.filter(item => item !== ready.statement), ready.statement].slice(-historyLimit));
    const printed: ConsoleSettings = { ...settings, expanded: settings.expanded || ready.expandedOnce };
    // The echoed line keeps its own prompt, so scrolling back reads like a
    // session rather than a list of results.
    setEntries(current => [
      ...current,
      { id: nextId.current++, blocks: buffered.map((text, index) => ({ kind: "echo" as const, prompt: index === 0 ? `${label}=> ` : "-> ", text })), expanded: printed.expanded, timing: printed.timing },
    ]);
    const controller = new AbortController();
    abort.current = controller;
    setRunning(true);
    const context: RunContext = {
      spec,
      engine: connection.engine,
      connectionId,
      database,
      argument: "",
      settings,
      backup: rowBackupEnabled(),
      signal: controller.signal,
      schema,
      databases,
      setDatabase,
      setSettings,
      clear: () => setEntries([]),
      quit: () => navigate("/editor"),
      connectionName: connection.name,
    };
    try {
      print(await runLine(context, ready.statement), printed);
    } catch (error) {
      if (controller.signal.aborted) {
        print([{ kind: "error", text: "Cancelled. For a write, check the database before retrying: cancelling does not prove it was rolled back." }], printed);
      } else if (error instanceof ApiError) {
        // A denial carries the rule that denied it; losing that would leave
        // the person guessing which policy to ask about.
        print([{ kind: "error", text: error.body.message, detail: error.body.policyId ? `Policy: ${error.body.policyId}` : undefined }], printed);
      } else {
        print([{ kind: "error", text: error instanceof Error ? error.message : "The statement failed." }], printed);
      }
    } finally {
      abort.current = null;
      setRunning(false);
      input.current?.focus();
    }
  }, [spec, connection, running, lines, draft, settings, database, label, connectionId, schema, databases, print, navigate]);

  const onKeyDown = (event: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      void submit();
      return;
    }
    // Ctrl+C abandons what is typed, and stops a statement if one is running,
    // which is what it does in every one of these clients.
    if (event.key === "c" && (event.ctrlKey || event.metaKey) && !window.getSelection()?.toString()) {
      event.preventDefault();
      abort.current?.abort();
      setDraft("");
      setLines([]);
      return;
    }
    if (event.key === "l" && event.ctrlKey) {
      event.preventDefault();
      setEntries([]);
      return;
    }
    if (event.key === "ArrowUp" && !event.shiftKey && history.length) {
      event.preventDefault();
      const index = historyAt === null ? history.length - 1 : Math.max(0, historyAt - 1);
      setHistoryAt(index);
      setDraft(history[index]);
      return;
    }
    if (event.key === "ArrowDown" && !event.shiftKey && historyAt !== null) {
      event.preventDefault();
      const index = historyAt + 1;
      if (index >= history.length) {
        setHistoryAt(null);
        setDraft("");
        return;
      }
      setHistoryAt(index);
      setDraft(history[index]);
    }
  };

  const banner = spec && connection
    ? `Rowset console — ${connection.name} (${connection.engine}), modelled on ${spec.client}. Every statement goes through the same guardrails and audit as the Query editor.`
    : "";

  if (connections.isPending) return <p className="p-6 text-sm">Loading connections…</p>;
  if (connections.isError) return <div className="p-6 text-sm">Could not load connections. <button className="underline" onClick={() => void connections.refetch()}>Retry</button></div>;
  if (!connections.data.length) {
    return (
      <div className="p-6 text-sm text-slate-600 dark:text-slate-300">
        Add a connection first, then open the console. <button className="underline" onClick={() => navigate("/connections")}>Connections</button>
      </div>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex min-h-9 flex-wrap items-center gap-2 border-b border-slate-200 bg-white px-3 py-1 text-xs text-slate-500 dark:border-slate-800 dark:bg-slate-950">
        <Icon name="terminal" className="text-slate-400" />
        <select
          aria-label="Connection"
          className="h-6 rounded border border-slate-200 bg-white px-1 text-slate-700 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200"
          value={connectionId}
          onChange={event => setConnectionId(event.target.value)}
        >
          {connections.data.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
        </select>
        {connection && <EnvBadge env={connection.environment} />}
        {connection && <span>{connection.engine}</span>}
        {database && <span className="text-slate-400">· {database}</span>}
        <div className="ml-auto flex items-center gap-2">
          {connection?.readOnly && <span className="text-amber-700 dark:text-amber-400">read-only</span>}
          {settings.timing && <span>timing</span>}
          {settings.expanded && <span>expanded</span>}
          {running && (
            <button type="button" className="rounded border border-slate-200 px-2 font-medium hover:bg-slate-50 dark:border-slate-700 dark:hover:bg-slate-800" onClick={() => abort.current?.abort()}>
              Stop
            </button>
          )}
        </div>
      </div>
      <div
        className="min-h-0 flex-1 overflow-auto bg-white px-3 py-2 dark:bg-slate-950"
        onClick={() => input.current?.focus()}
      >
        {!spec && connection && (
          <p className="font-mono text-[12.5px] text-amber-700 dark:text-amber-400">
            {connection.engine} has no console yet. Use the Query editor for this connection.
          </p>
        )}
        {spec && (
          <>
            <p className="pb-2 font-mono text-[12.5px] text-slate-500 dark:text-slate-400">{banner}</p>
            <ConsoleOutput entries={entries} />
            <div className="font-mono text-[12.5px]">
              {lines.map((text, index) => (
                <div key={index} className="whitespace-pre leading-[1.45]">
                  <span className="text-brand-500">{index === 0 ? `${label}=> ` : "-> "}</span>
                  <span className="text-slate-700 dark:text-slate-100">{text}</span>
                </div>
              ))}
              <div className="flex flex-wrap items-start leading-[1.45]">
                <span className="whitespace-pre text-brand-500">{prompt}</span>
                <textarea
                  ref={input}
                  autoFocus
                  rows={1}
                  spellCheck={false}
                  autoComplete="off"
                  aria-label="Console input"
                  disabled={running}
                  className="min-w-[12rem] flex-1 resize-none border-0 bg-transparent p-0 font-mono text-[12.5px] leading-[1.45] text-slate-800 outline-none disabled:opacity-60 dark:text-slate-100"
                  value={draft}
                  onChange={event => setDraft(event.target.value.replace(/\n/g, ""))}
                  onKeyDown={onKeyDown}
                />
              </div>
            </div>
            <div ref={bottom} />
          </>
        )}
      </div>
      {spec && (
        <div className="flex min-h-7 items-center gap-3 border-t border-slate-200 bg-white px-3 text-[11px] text-slate-400 dark:border-slate-800 dark:bg-slate-950">
          <span>{spec.hint}</span>
          <span className="ml-auto">↑ history · Shift+Enter newline · Ctrl+C cancel · Ctrl+L clear</span>
        </div>
      )}
    </div>
  );
}
