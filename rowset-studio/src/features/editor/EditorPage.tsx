import QueryParameters from "./ParameterFields";
import { parameterNames, resolveParameters, type QueryParameters as ParameterValues } from "./queryParameters";
import { lazy, Suspense, useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useBlocker, useLocation, useNavigate } from "react-router";
import { Button, Modal, Panel } from "../../components/ui";
import { Icon, type IconName } from "../../components/Icon";
import { envFrame, envKind, envRail } from "../../components/EnvBadge";
import { useEditorStore } from "../../stores/editorStore";
import { listConnections } from "../connections/api";
import HistoryPanel from "./HistoryPanel";
import PolicyBanner from "./PolicyBanner";
import ResultsGrid, { type ResultEditing } from "./ResultsGrid";
import RunToolbar, { type WorkspaceStatus } from "./RunToolbar";
import SaveToNotebookDialog from "../notebooks/SaveToNotebookDialog";
import PlanPanel, { type PlanState } from "../plan/PlanPanel";
import ExplorerPanel from "./ExplorerPanel";
import { explainQuery, exportTable, getSchema, listDatabases, runOnConnections, runQuery, beginTxn, txnQuery, commitTxn, rollbackTxn, type QueryResult, type SchemaInfo } from "./api";
import { buildSqlCompletions } from "./sqlCompletions";
import { useSchema } from "./useEditor";
import { formatSql, statementAt, splitStatements } from "./sqlText";
import { useAuth } from "../../lib/auth";
import { SchemaActions } from "./schemaActions";
import { mongoQuery, mongoRequest, formatMongoQuery } from "./mongoQuery";
import MongoQueryBar from "./MongoQueryBar";
import RedisQueryBar, { redisQuery } from "./RedisQueryBar";
import ElasticsearchQueryBar, { elasticsearchQuery } from "./ElasticsearchQueryBar";
import SnippetsMenu from "./SnippetsMenu";
import { api, ApiError } from "../../lib/api";
import { useEngineCapabilities, useShared } from "../../lib/instance";
import { rowBackupEnabled } from "../../lib/preferences";
import { useActiveExtensions, type DenialContext } from "../../app/extensions";
import WorkspaceGate, { exportWorkspace, useWorkspacePersistence } from "./WorkspaceGate";
import AssistantPanel from "../ai/AssistantPanel";
import MultiRunDialog from "./MultiRunDialog";
import MultiRunPanel, { type MultiRunState } from "./MultiRunPanel";
import { applyEvent, failOutcomes, initialOutcomes, scriptChanges, stopOutcomes, type MultiRunTarget, type TargetOutcome } from "./multiRun";
import { mergeWorkspace, type WorkspaceDocument, type WorkspaceSnapshot, type WorkspaceTab } from "./workspace";

type BottomTab = "results" | "history" | "messages" | "plan" | "connections";

type QueryTab = WorkspaceTab;

// Each workspace tab owns its own run: starting a query in one tab never
// blocks or overwrites another tab's in-flight run, result or status line.
interface TabRunState {
  status: "idle" | "running" | "success" | "error" | "pending";
  /** The statement of the latest single run. */
  sql?: string;
  data?: QueryResult;
  error?: Error;
  startedAt?: number;
  endedAt?: number;
  message: string;
  messageError: boolean;
  /** One entry per statement when several ran in sequence. */
  results?: StatementResult[];
  activeResult?: number;
}

interface StatementResult {
  sql: string;
  status: "success" | "error" | "pending";
  data?: QueryResult;
  error?: Error;
  message: string;
}

interface RowEditing {
  engine: string;
  primaryKey: ResultEditing["primaryKey"];
  onApply: (statements: string[], sourceSql: string) => void;
}

// Rows changed by an UPDATE or DELETE are backed up first when possible.
function backupNote(result: QueryResult) {
  const backup = result.annotations?.backup as { rows?: number } | undefined;
  const skipped = result.annotations?.backupSkipped as string | undefined;
  if (backup?.rows) return ` Backed up ${backup.rows} row(s) first; restore them from Activity → Row backups.`;
  if (skipped) return ` No backup was taken: ${skipped}.`;
  return "";
}

function editingFor(rowEditing: RowEditing | undefined, sourceSql: string | undefined): ResultEditing | undefined {
  if (!rowEditing || !sourceSql) return undefined;
  return { engine: rowEditing.engine, primaryKey: rowEditing.primaryKey, onApply: (statements) => rowEditing.onApply(statements, sourceSql) };
}

const IDLE_RUN: TabRunState = { status: "idle", message: "Ready", messageError: false };

// Auto-refresh is a client-side convenience gate, not a security boundary
// (guardrail policies still run on every request either way) - it just
// keeps the option from quietly turning a write into a recurring one. Mongo,
// Redis and Elasticsearch query bars can only ever build a read request, so
// they're always eligible; everything else needs a recognizably read-only
// leading keyword.
function looksReadOnly(sql: string, engine?: string): boolean {
  if (engine === "mongodb" || engine === "redis" || engine === "valkey" || engine === "elasticsearch") return true;
  const withoutComments = sql.replace(/--[^\n]*/g, "").replace(/\/\*[\s\S]*?\*\//g, "");
  const first = withoutComments.trim().split(/[\s(]/, 1)[0]?.toUpperCase() ?? "";
  // EXEC/EXECUTE/CALL is deliberately allowed even though a stored procedure
  // could write - this is how someone watches a read-only diagnostic proc
  // (sp_whoisactive, sp_who2, ...) on an interval, the whole reason this
  // feature exists. Plain DML/DDL keywords stay excluded.
  return ["SELECT", "WITH", "SHOW", "EXPLAIN", "DESC", "DESCRIBE", "PRAGMA", "EXEC", "EXECUTE", "CALL"].includes(first);
}

function isNodeAnnotation(value: unknown): value is { host: string; role?: string } {
  return typeof value === "object" && value !== null && typeof (value as { host?: unknown }).host === "string";
}

const MonacoSqlEditor = lazy(() => import("./MonacoSqlEditor"));

export default function EditorPage() {
  const user = useAuth(state => state.user);
  return <WorkspaceGate key={`${user?.orgId}:${user?.userId}`}>{(snapshot, initial) => <EditorWorkspace snapshot={snapshot} initial={initial} />}</WorkspaceGate>;
}

function EditorWorkspace({ snapshot, initial }: { snapshot: WorkspaceSnapshot; initial: WorkspaceDocument }) {
  const { activeConnectionId: rememberedConnectionId, setActiveConnection } = useEditorStore();
  const [tabs, setTabs] = useState<QueryTab[]>(initial.tabs);
  const [plans, setPlans] = useState<Record<string, PlanState>>({});
  const [activeTabId, setActiveTabId] = useState(initial.activeTabId);
  // Activity's "Open in editor" navigates here with the SQL; open it once as
  // a new tab, then drop the navigation state so reloads do not repeat it.
  const location = useLocation();
  const navigate = useNavigate();
  // Scheduled queries write files on this computer: personal workspaces only.
  const shared = useShared();
  const openedFromNavigation = useRef<string | null>(null);
  useEffect(() => {
    const state = location.state as { openSql?: string; connectionId?: string | null; database?: string; title?: string; restoreOf?: string; openConnection?: boolean } | null;
    if (!state || openedFromNavigation.current === location.key) return;
    // Jumping to a connection (e.g. from the command palette) opens a fresh
    // tab bound to it, since the active tab may already be bound elsewhere
    // and the store's "remembered" connection only fills in blank tabs.
    if (state.openConnection && state.connectionId) {
      openedFromNavigation.current = location.key;
      const connection = connections.find((item) => item.id === state.connectionId);
      const id = crypto.randomUUID();
      setTabs((docs) => [...docs, { id, title: "New Query", sql: defaultSql(connection?.engine), connectionId: state.connectionId ?? null, database: connection?.database ?? "" }]);
      setActiveTabId(id);
      setActiveConnection(state.connectionId);
      navigate(location.pathname, { replace: true, state: null });
      return;
    }
    if (!state.openSql) return;
    openedFromNavigation.current = location.key;
    const id = crypto.randomUUID();
    const sql = state.openSql;
    setTabs((docs) => [...docs, { id, title: state.title?.slice(0, 80) || "From activity", sql, connectionId: state.connectionId ?? null, database: state.database ?? "", ...(state.restoreOf ? { restoreOf: state.restoreOf } : {}) }]);
    setActiveTabId(id);
    navigate(location.pathname, { replace: true, state: null });
  }, [location, navigate]);
  const workspace: WorkspaceDocument = { version: 1, tabs, activeTabId: tabs.some(tab => tab.id === activeTabId) ? activeTabId : tabs[0].id };
  const persistence = useWorkspacePersistence(snapshot, initial, workspace);
  const [bottomTab, setBottomTab] = useState<BottomTab>("results");
  // The assistant sits beside the editor, where it can stay open while a
  // statement runs and its results come in.
  const [assistantOpen, setAssistantOpen] = useState(() => localStorage.getItem("rowset.editor.assistant") === "open");
  const [runStates, setRunStates] = useState<Record<string, TabRunState>>({});
  // A statement the server would not run because its rows cannot be backed up.
  const [backupPrompt, setBackupPrompt] = useState<{ tabId: string; sql: string; reason: string } | null>(null);
  const [explorerOpen, setExplorerOpen] = useState(() => localStorage.getItem("rowset.editor.explorer") !== "collapsed");
  const [selectedSql, setSelectedSql] = useState("");
  const [cursor, setCursor] = useState({ line: 1, column: 1 });
  // Each tab mounts its own editor; a selection or cursor from the previous
  // tab must never decide what Run or Explain uses here.
  useEffect(() => {
    setSelectedSql("");
    setCursor({ line: 1, column: 1 });
  }, [activeTabId]);
  const [bottomHeight, setBottomHeight] = useState(() => Number(localStorage.getItem("rowset.editor.bottom")) || 280);
  const [explorerWidth, setExplorerWidth] = useState(() => Number(localStorage.getItem("rowset.editor.explorerWidth")) || 286);
  // Monotonic per-tab run counter: a stale response (tab re-run before the
  // previous request settled) is dropped instead of clobbering the newer one.
  const runSeq = useRef<Record<string, number>>({});
  const controllers = useRef<Record<string, AbortController>>({});
  const planControllers = useRef<Record<string, AbortController>>({});
  const scripts = useRef<Record<string, boolean>>({});
  const transactions = useRef<Record<string, { id: string; connectionId: string; database: string }>>({});
  const [transactionIDs, setTransactionIDs] = useState<Record<string, string>>({});
  const [abortedTransactions, setAbortedTransactions] = useState<Record<string, boolean>>({});
  const [saveDialogOpen, setSaveDialogOpen] = useState(false);
  // A script run on several connections, one per tab.
  const [multiRunDialog, setMultiRunDialog] = useState<{ sql: string; source: "selection" | "editor" } | null>(null);
  const [multiRuns, setMultiRuns] = useState<Record<string, MultiRunState>>({});
  const multiControllers = useRef<Record<string, AbortController>>({});
  // Manual-commit tabs open a transaction lazily with their next statement.
  const [manualCommitTabs, setManualCommitTabs] = useState<Record<string, boolean>>({});
  // Auto-refresh: 0 = off, otherwise the repeat interval in ms, per tab.
  const [autoRefreshTabs, setAutoRefreshTabs] = useState<Record<string, number>>({});
  const manualCommit = useRef<Record<string, boolean>>({});
  const [pendingStatements, setPendingStatements] = useState<Record<string, number>>({});
  const lastOutcome = useRef<Record<string, Omit<StatementResult, "sql">>>({});
  const [transactionBusy, setTransactionBusy] = useState(false);
  // State alone cannot lock operations before React's next render.
  const transactionOperations = useRef(new Set<string>());
  const closingTabs = useRef(new Set<string>());
  const mounted = useRef(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const workspaceFileInput = useRef<HTMLInputElement>(null);
  const tabsRef = useRef(tabs);
  tabsRef.current = tabs;
  const activeTab = tabs.find((tab) => tab.id === activeTabId) ?? tabs[0];
  const activeTabConnectionId = activeTab?.connectionId;
  const activeTabDatabase = activeTab?.database;
  const activeTabNodeRole = activeTab?.nodeRole;
  const activeConnectionId = activeTabConnectionId ?? rememberedConnectionId;
  const queryClient = useQueryClient();

  useEffect(() => {
    localStorage.setItem("rowset.editor.bottom", String(bottomHeight));
  }, [bottomHeight]);

  useEffect(() => {
    localStorage.setItem("rowset.editor.explorerWidth", String(explorerWidth));
  }, [explorerWidth]);

  useEffect(() => {
    localStorage.setItem("rowset.editor.explorer", explorerOpen ? "expanded" : "collapsed");
  }, [explorerOpen]);

  useEffect(() => {
    localStorage.setItem("rowset.editor.assistant", assistantOpen ? "open" : "closed");
  }, [assistantOpen]);


  useEffect(() => {
    if (!tabs.some((t) => t.id === activeTabId) && tabs[0]) {
      setActiveTabId(tabs[0].id);
    }
  }, [activeTabId, tabs]);

  const currentSql = activeTab?.sql ?? "";

  useEffect(() => {
    mounted.current = true;
    const leaving = (event: BeforeUnloadEvent) => {
      if (Object.keys(transactions.current).length || Object.keys(controllers.current).length || Object.keys(multiControllers.current).length || transactionOperations.current.size || Object.values(scripts.current).some(Boolean)) { event.preventDefault(); event.returnValue = ""; }
    };
    window.addEventListener("beforeunload", leaving);
    const running = controllers.current, txns = transactions.current, batches = scripts.current, multi = multiControllers.current, planRequests = planControllers.current;
    return () => {
      mounted.current = false;
      Object.keys(batches).forEach(id => { batches[id] = false; });
      window.removeEventListener("beforeunload", leaving);
      Object.values(running).forEach(c => c.abort());
      Object.values(multi).forEach(c => c.abort());
      Object.values(planRequests).forEach(c => c.abort());
      Object.values(txns).forEach(t => void rollbackTxn(t.connectionId, t.id).catch(() => undefined));
    };
  }, []);

  const { data: connections = [] } = useQuery({
    queryKey: ["connections"],
    queryFn: listConnections,
    staleTime: 60_000,
  });
  const activeConnection = connections.find((c) => c.id === activeConnectionId) ?? null;
  const capabilities = useEngineCapabilities(activeConnection?.engine);
  const [tabParameters, setTabParameters] = useState<Record<string, ParameterValues>>({});
  const isMongo = activeConnection?.engine === "mongodb";
  const isCassandra = activeConnection?.engine === "cassandra";
  const isElasticsearch = activeConnection?.engine === "elasticsearch";
  const isRedis = activeConnection?.engine === "redis" || activeConnection?.engine === "valkey";
  const parameters = tabParameters[activeTabId] ?? {};
  const parameterList = isMongo ? [] : parameterNames(currentSql, activeConnection?.engine ?? "");
  const selectedDb = activeTabDatabase || activeConnection?.database || defaultDatabase(activeConnection?.engine);
  const selectedNodeRole = activeConnection?.nodePolicy === "primary_only" ? "primary"
    : activeConnection?.nodePolicy === "secondary_only" ? "secondary"
    : activeTabNodeRole ?? activeConnection?.defaultNodeRole ?? "primary";
  const activeRun = runStates[activeTabId] ?? IDLE_RUN;
  const selectionStatements = useMemo(() => (selectedSql.trim() ? splitStatements(selectedSql, activeConnection?.engine).length : 0), [selectedSql, activeConnection?.engine]);

  useEffect(() => {
    if (connections.length === 0 || !activeTab) return;
    const connection = connections.find((item) => item.id === activeConnectionId) ?? connections[0];
    if (activeTabConnectionId !== connection.id || !activeTabDatabase || (connection.engine === "mongodb" && !activeTab.sql.trim())) {
      setTabs((current) => current.map((tab) => tab.id === activeTab.id ? {
        ...tab,
        connectionId: connection.id,
        sql: !tab.sql.trim() ? defaultSql(connection.engine) : tab.sql,
        database: activeTabDatabase || connection.database || defaultDatabase(connection.engine),
      } : tab));
    }
    if (rememberedConnectionId !== connection.id) setActiveConnection(connection.id);
  }, [activeConnectionId, activeTab?.id, activeTabConnectionId, activeTabDatabase, connections, rememberedConnectionId, setActiveConnection]);

  // Feed the structured schema to the editor's autocomplete: per-table columns
  // for alias resolution, tables per schema, plus callable routines.
  const { data: schema } = useSchema(activeConnectionId, selectedDb || undefined);
  const { data: databaseList = [] } = useQuery({
    queryKey: ["databases", activeConnectionId],
    queryFn: () => listDatabases(activeConnectionId!),
    enabled: Boolean(activeConnectionId),
  });
  const cachedSchemas = useQueries({
    queries: databaseList.map((database) => ({
      queryKey: ["schema", activeConnectionId, database],
      queryFn: () => getSchema(activeConnectionId!, database),
      enabled: false,
      staleTime: 5 * 60_000,
    })),
  });
  useEffect(() => {
    if (!activeConnectionId || databaseList.length === 0) return;
    let cancelled = false;
    void (async () => {
      for (const database of databaseList) {
        if (cancelled || database === selectedDb) continue;
        await queryClient.prefetchQuery({
          queryKey: ["schema", activeConnectionId, database],
          queryFn: () => getSchema(activeConnectionId, database),
          staleTime: 5 * 60_000,
        });
      }
    })();
    return () => { cancelled = true; };
  }, [activeConnectionId, databaseList, queryClient, selectedDb]);
  const loadedSchemas = useMemo(() => Object.fromEntries(cachedSchemas.flatMap((query, index) => query.data ? [[databaseList[index], query.data as SchemaInfo]] : [])), [cachedSchemas, databaseList]);
  const completions = useMemo(() => buildSqlCompletions(schema, databaseList, activeConnection?.engine ?? "", loadedSchemas), [schema, databaseList, activeConnection?.engine, loadedSchemas]);

  // The browser's Back button always asks before leaving the editor; a link
  // asks only when leaving would stop a query or roll back a transaction.
  const runningTabs = Object.values(runStates).filter((run) => run.status === "running").length + Object.values(multiRuns).filter((run) => run.running).length;
  const openTransactions = Object.keys(transactionIDs).length;
  const leaveByBack = useRef(false);
  const leaveBlocker = useBlocker(({ currentLocation, nextLocation, historyAction }) => {
    if (currentLocation.pathname === nextLocation.pathname) return false;
    leaveByBack.current = historyAction === "POP";
    return leaveByBack.current || runningTabs > 0 || openTransactions > 0;
  });

  // Opens SQL the assistant wrote in a tab of its own, so nothing you typed
  // is replaced.
  function openSqlTab(sql: string, title: string) {
    const id = crypto.randomUUID();
    setTabs((docs) => [...docs, { id, title, sql, connectionId: activeConnectionId, database: selectedDb }]);
    setActiveTabId(id);
  }

  function patchRun(tabId: string, patch: Partial<TabRunState>) {
    if (closingTabs.current.has(tabId)) return;
    setRunStates((current) => ({ ...current, [tabId]: { ...(current[tabId] ?? IDLE_RUN), ...patch } }));
  }

  function forgetTransaction(tabId: string) {
    delete transactions.current[tabId];
    setTransactionIDs(current => { const next = { ...current }; delete next[tabId]; return next; });
    setAbortedTransactions(current => { if (!(tabId in current)) return current; const next = { ...current }; delete next[tabId]; return next; });
    setPendingStatements(current => { if (!(tabId in current)) return current; const next = { ...current }; delete next[tabId]; return next; });
  }

  function setActiveMessage(text: string, isError: boolean) {
    if (activeTabId) patchRun(activeTabId, { message: text, messageError: isError });
  }

  function updateActiveSql(sql: string) {
    setTabs((docs) => docs.map((doc) => (doc.id === activeTabId ? { ...doc, sql } : doc)));
  }

  function addTab() {
    const next = `q-${Date.now()}`;
    setTabs((docs) => [
      ...docs,
      {
        id: next,
        title: `Query ${docs.length + 1}`,
        sql: defaultSql(activeConnection?.engine),
        connectionId: activeConnectionId,
        database: selectedDb,
      },
    ]);
    setActiveTabId(next);
    setBottomTab("results");
    patchRun(next, { message: "New query tab created", messageError: false });
  }

  async function closeTab(id: string) {
    if (tabs.length === 1 || transactionOperations.current.has(id) || closingTabs.current.has(id)) return;
    if ((transactions.current[id] || controllers.current[id] || multiControllers.current[id] || scripts.current[id]) && !window.confirm("This tab has an active query or transaction. Cancel and roll back before closing?")) return;
    closingTabs.current.add(id);
    scripts.current[id] = false;
    controllers.current[id]?.abort();
    multiControllers.current[id]?.abort();
    planControllers.current[id]?.abort();
    // Invalidate callbacks immediately, including while rollback is in flight.
    delete runSeq.current[id];
    const tx = transactions.current[id];
    if (tx) {
      try { await rollbackTxn(tx.connectionId, tx.id); }
      catch (error) {
        if (!(error instanceof ApiError && error.body.code === "TXN_NOT_FOUND")) {
          if (error instanceof ApiError && error.body.code === "TXN_FINISH_ERROR") forgetTransaction(id);
          closingTabs.current.delete(id); patchRun(id, { status: "error", message: error instanceof Error ? error.message : "Rollback failed", messageError: true }); return;
        }
      }
      delete transactions.current[id];
      setTransactionIDs(current => { const next = { ...current }; delete next[id]; return next; });
    }
    setTabs((docs) => docs.filter((doc) => doc.id !== id));
    delete runSeq.current[id];
    setMultiRuns((current) => { if (!(id in current)) return current; const next = { ...current }; delete next[id]; return next; });
    setPlans((current) => { if (!(id in current)) return current; const next = { ...current }; delete next[id]; return next; });
    setRunStates((current) => {
      if (!(id in current)) return current;
      const next = { ...current };
      delete next[id];
      return next;
    });
    if (activeTabId === id) {
      const next = tabs.find((doc) => doc.id !== id) ?? tabs[0];
      if (next) setActiveTabId(next.id);
    }
  }

  function formatCurrent() {
    try { updateActiveSql(isMongo ? formatMongoQuery(currentSql) : formatSql(currentSql)); }
    catch { setActiveMessage("Invalid JSON: check the query before formatting.", true); return; }
    setActiveMessage("Formatted locally", false);
    setBottomTab("messages");
  }

  async function openSQLFile(file?: File) {
    if (!file) return;
    if (file.size > 5 * 1024 * 1024) { setActiveMessage("SQL files must be smaller than 5 MB.", true); return; }
    try {
      const sql = await file.text(), id = crypto.randomUUID();
      setTabs(docs => [...docs, { id, title: file.name, sql, connectionId: activeConnectionId, database: selectedDb }]);
      setActiveTabId(id);
    } catch (error) { setActiveMessage(error instanceof Error ? error.message : "Unable to open SQL file", true); }
  }

  async function importWorkspace(file?: File) {
    if (!file) return;
    try {
      if (file.size > 768 * 1024) throw new Error("Workspace files must be smaller than 768 KB.");
      const recovered: unknown = JSON.parse(await file.text());
      // Append only: running query/transaction tabs retain their identity.
      if (!mounted.current) return;
      const current = tabsRef.current;
      const merged = mergeWorkspace({ version: 1, tabs: current, activeTabId: current[0].id }, recovered, () => crypto.randomUUID());
      setTabs(merged.tabs);
      setActiveTabId(merged.activeTabId);
    } catch (error) { setActiveMessage(error instanceof Error ? error.message : "Workspace import failed", true); }
  }

  function saveSQLFile() {
    const isJSON = isMongo || isRedis || isElasticsearch;
    const extension = isJSON ? "json" : isCassandra ? "cql" : "sql";
    const url = URL.createObjectURL(new Blob([currentSql], { type: isJSON ? "application/json;charset=utf-8" : "text/plain;charset=utf-8" }));
    const anchor = document.createElement("a"); anchor.href = url;
    anchor.download = `${(activeTab?.title ?? "query").replace(/\.(sql|json|cql)$/i, "").replace(/[\\/:*?"<>|]/g, "_")}.${extension}`;
    anchor.click(); window.setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  function saveCurrent() {
    if ((selectedSql.trim() || currentSql).trim()) setSaveDialogOpen(true);
    else setActiveMessage("Nothing to save: the editor is empty.", true);
  }

  async function execute(sql: string, script = false, skipBackup = false) {
    const tabId = activeTab?.id;
    const connectionId = activeConnectionId;
    if (!mounted.current || !tabId || !connectionId || !sql.trim() || closingTabs.current.has(tabId) || transactionOperations.current.has(tabId) || controllers.current[tabId] || (!script && scripts.current[tabId])) return false;
    if (!isMongo) {
      try { sql = resolveParameters(sql, activeConnection?.engine ?? "", parameters); }
      catch (error) { setActiveMessage((error as Error).message, true); setBottomTab("messages"); return false; }
    }
    const database = selectedDb || undefined;
    const nodeRole = selectedNodeRole;
    const seq = (runSeq.current[tabId] ?? 0) + 1;
    runSeq.current[tabId] = seq;
    const controller = new AbortController();
    controllers.current[tabId] = controller;

    setBottomTab("results");
    // A plan belongs to the statement it explained; a new run replaces it.
    setPlans((current) => {
      if (!current[tabId]) return current;
      const next = { ...current };
      delete next[tabId];
      return next;
    });
    patchRun(tabId, {
      status: "running",
      sql,
      data: undefined,
      error: undefined,
      startedAt: Date.now(),
      endedAt: undefined,
      message: "Running query…",
      messageError: false,
      ...(script ? {} : { results: undefined, activeResult: undefined }),
    });

    let tx = transactions.current[tabId];
    if (!isMongo && !tx && manualCommit.current[tabId]) {
      try {
        const started = await beginTxn(connectionId, selectedDb);
        tx = { id: started.txnId, connectionId, database: selectedDb };
        if (!mounted.current || runSeq.current[tabId] !== seq) {
          await rollbackTxn(connectionId, started.txnId).catch(() => undefined);
          return false;
        }
        transactions.current[tabId] = tx;
        setTransactionIDs(current => ({ ...current, [tabId]: started.txnId }));
      } catch (err) {
        if (controllers.current[tabId] === controller) delete controllers.current[tabId];
        const error = err instanceof Error ? err : new Error("Could not start transaction");
        const message = `Could not start the manual-commit transaction: ${error.message}`;
        lastOutcome.current[tabId] = { status: "error", error, message };
        patchRun(tabId, { status: "error", error, endedAt: Date.now(), message, messageError: true });
        return false;
      }
    }
    const progress = (data: QueryResult) => {
      if (runSeq.current[tabId] === seq) patchRun(tabId, { data, message: `Receiving rows… ${data.rowCount}` });
    };
    // A restore script puts rows back; backing it up again would only add noise.
    const backup = !skipBackup && !shared && !activeTab?.restoreOf && rowBackupEnabled();
    const runMongo = async (): Promise<QueryResult> => {
      const response = await api<{ documents: unknown[]; truncated: boolean; durationMs: number; limit: number }>(`/connections/${connectionId}/documents/find`, { method: "POST", signal: controller.signal, body: mongoRequest(sql, selectedDb) });
      return { columns: ["document"], columnTypes: ["BSON"], rows: response.documents.map(document => [document]), rowCount: response.documents.length, durationMs: response.durationMs, truncated: response.truncated, policyNotice: response.truncated ? `Document limit: ${response.limit}` : undefined };
    };
    const runElasticsearch = async (): Promise<QueryResult> => {
      const response = await api<{ documents: unknown[]; truncated: boolean; durationMs: number; limit: number }>(`/connections/${connectionId}/elasticsearch/search`, { method: "POST", signal: controller.signal, body: sql });
      return { columns: ["document"], columnTypes: ["JSON"], rows: response.documents.map(document => [document]), rowCount: response.documents.length, durationMs: response.durationMs, truncated: response.truncated, policyNotice: response.truncated ? `Result size: ${response.limit}` : undefined };
    };
    const runRedis = async (): Promise<QueryResult> => {
      const input = JSON.parse(sql) as { pattern?: string; type?: string; limit?: number };
      const body = JSON.stringify({ database: selectedDb, pattern: input.pattern, type: input.type, limit: input.limit });
      const response = await api<{ entries: { key: string; type: string; ttl: number; value: unknown }[]; cursor: number; durationMs: number; limit: number }>(`/connections/${connectionId}/redis/scan`, { method: "POST", signal: controller.signal, body });
      return { columns: ["key", "type", "ttl", "value"], rows: response.entries.map(e => [e.key, e.type, e.ttl, e.value]), rowCount: response.entries.length, durationMs: response.durationMs, truncated: response.cursor !== 0, policyNotice: response.cursor !== 0 ? `Scan limit: ${response.limit}; more keys remain (cursor ${response.cursor})` : undefined };
    };
    const runCassandra = async (): Promise<QueryResult> => {
      const response = await api<{ columns: string[]; rows: unknown[][]; durationMs: number; truncated: boolean }>(`/connections/${connectionId}/cassandra/query`, { method: "POST", signal: controller.signal, body: JSON.stringify({ keyspace: selectedDb, query: sql, limit: 1000 }) });
      return { columns: response.columns ?? [], rows: response.rows ?? [], rowCount: (response.rows ?? []).length, durationMs: response.durationMs, truncated: response.truncated };
    };
    return (isMongo ? runMongo() : isElasticsearch ? runElasticsearch() : isRedis ? runRedis() : isCassandra ? runCassandra() : tx ? txnQuery(tx.connectionId, tx.id, sql, tx.database, controller.signal, progress, backup) : runQuery(connectionId, sql, database, nodeRole, controller.signal, progress, backup))
      .then((res) => {
        if (runSeq.current[tabId] !== seq) return false;
        patchRun(tabId, {
          status: "success",
          data: res,
          error: undefined,
          endedAt: Date.now(),
          message: `Query returned ${res.rowCount} row(s).${backupNote(res)}`,
          messageError: false,
        });
        lastOutcome.current[tabId] = { status: "success", data: res, message: `Query returned ${res.rowCount} row(s).${backupNote(res)}` };
        if (tx) setPendingStatements(current => ({ ...current, [tabId]: (current[tabId] ?? 0) + 1 }));
        if (/\b(create|alter|drop|truncate)\b/i.test(sql)) void queryClient.invalidateQueries({ queryKey: ["schema", connectionId] });
        return true;
      })
      .catch(async (err) => {
        if (runSeq.current[tabId] !== seq) return false;
        const error = err instanceof Error ? err : new Error("Query failed");
        const pending = err instanceof ApiError && err.body.code === "PENDING";
        if (err instanceof ApiError && err.body.code === "BACKUP_UNAVAILABLE") setBackupPrompt({ tabId, sql, reason: err.body.message });
        let message = controller.signal.aborted ? "Cancellation requested. For writes, check the database before retrying; cancellation does not prove a write was rolled back." : error.message;
        const txState = err instanceof ApiError ? err.body.transactionState : undefined;
        if (tx && controller.signal.aborted) {
          // Every bundled driver closes the pinned connection when a statement
          // is cancelled, so the server-side transaction cannot continue.
          await rollbackTxn(tx.connectionId, tx.id).catch(() => undefined);
          forgetTransaction(tabId);
          message = "Statement stopped. The transaction ended and the database discarded its uncommitted changes.";
        } else if (err instanceof ApiError && err.body.code === "TXN_NOT_FOUND") {
          forgetTransaction(tabId);
        } else if (tx && txState === "lost") {
          forgetTransaction(tabId);
          message = `${error.message}\nThe transaction's database connection ended and its uncommitted changes were discarded. Run again to start a new transaction.`;
        } else if (tx && txState === "aborted") {
          setAbortedTransactions(current => ({ ...current, [tabId]: true }));
          message = `${error.message}\nThe database aborted this transaction. Roll back to continue; Commit would apply nothing.`;
        }
        if (!mounted.current || runSeq.current[tabId] !== seq) return false;
        lastOutcome.current[tabId] = { status: pending ? "pending" : "error", error, message };
        patchRun(tabId, {
          status: pending ? "pending" : "error",
          data: undefined,
          error,
          endedAt: Date.now(),
          message,
          messageError: !pending,
        });
        return false;
      })
      .finally(() => {
        if (controllers.current[tabId] === controller) delete controllers.current[tabId];
        queryClient.invalidateQueries({ queryKey: ["history", connectionId] });
      });
  }

  // Run: the selection (every statement in it) or the statement at the cursor.
  function onRun() {
    if (isMongo || isCassandra || isElasticsearch || isRedis) { void execute(selectedSql.trim() || currentSql); return; }
    const selected = selectedSql.trim();
    if (!selected) {
      const offset = currentSql.split("\n").slice(0, cursor.line - 1).reduce((n, line) => n + line.length + 1, 0) + cursor.column - 1;
      void execute(statementAt(currentSql, offset, activeConnection?.engine));
      return;
    }
    const statements = splitStatements(selected, activeConnection?.engine);
    if (statements.length > 1) void runStatements(statements);
    else void execute(selected);
  }

  const autoRefreshMs = autoRefreshTabs[activeTabId] ?? 0;
  const autoRefreshEligible = looksReadOnly(selectedSql.trim() || currentSql, activeConnection?.engine);
  // Refs so the interval always calls the latest onRun/runStates without
  // resetting the timer on every keystroke or result.
  const onRunRef = useRef(onRun);
  onRunRef.current = onRun;
  const runStatesRef = useRef(runStates);
  runStatesRef.current = runStates;
  const autoRefreshEligibleRef = useRef(autoRefreshEligible);
  autoRefreshEligibleRef.current = autoRefreshEligible;
  useEffect(() => {
    if (!autoRefreshMs || !activeConnectionId) return;
    const timer = window.setInterval(() => {
      // The statement may have been edited since auto-refresh was turned on
      // (e.g. from a SELECT into an UPDATE); stop rather than keep firing.
      if (!autoRefreshEligibleRef.current) { setAutoRefreshTabs((current) => ({ ...current, [activeTabId]: 0 })); return; }
      if (runStatesRef.current[activeTabId]?.status !== "running") onRunRef.current();
    }, autoRefreshMs);
    return () => window.clearInterval(timer);
    // Deliberately not depending on onRun/runStates - see the refs above.
  }, [autoRefreshMs, activeTabId, activeConnectionId]);

  // Explain the first selected statement, or the statement at the cursor.
  function statementUnderCursor() {
    const selected = selectedSql.trim();
    const offset = currentSql.split("\n").slice(0, cursor.line - 1).reduce((n, line) => n + line.length + 1, 0) + cursor.column - 1;
    return (selected ? splitStatements(selected, activeConnection?.engine)[0]?.sql ?? selected : statementAt(currentSql, offset, activeConnection?.engine)).trim();
  }

  async function onExplain(analyze: boolean) {
    if (isMongo) return;
    const tabId = activeTabId;
    const connectionId = activeConnectionId;
    let sql = statementUnderCursor();
    if (!connectionId || !sql) return;
    try { sql = resolveParameters(sql, activeConnection?.engine ?? "", parameters); }
    catch (error) { setActiveMessage((error as Error).message, true); setBottomTab("messages"); return; }
    planControllers.current[tabId]?.abort();
    const controller = new AbortController();
    planControllers.current[tabId] = controller;
    setBottomTab("plan");
    setPlans((current) => ({ ...current, [tabId]: { status: "loading", sql, analyze } }));
    try {
      const result = await explainQuery(connectionId, sql, { database: selectedDb || undefined, nodeRole: selectedNodeRole, analyze, signal: controller.signal });
      if (!mounted.current || closingTabs.current.has(tabId) || planControllers.current[tabId] !== controller) return;
      setPlans((current) => ({ ...current, [tabId]: { status: "ready", sql, analyze, result } }));
    } catch (err) {
      if (!mounted.current || closingTabs.current.has(tabId) || planControllers.current[tabId] !== controller) return;
      setPlans((current) => ({ ...current, [tabId]: { status: "error", sql, analyze, error: err instanceof Error ? err : new Error("Could not get the plan") } }));
    } finally {
      if (planControllers.current[tabId] === controller) delete planControllers.current[tabId];
    }
  }

  // The same statements on several connections, run by the server, which
  // applies policies, row backups and history to every connection on its own.
  async function startMultiRun(targets: MultiRunTarget[], statements: string[], concurrency: number) {
    const tabId = activeTabId;
    setMultiRunDialog(null);
    if (!tabId || multiControllers.current[tabId]) return;
    const controller = new AbortController();
    multiControllers.current[tabId] = controller;
    const backup = !shared && rowBackupEnabled();
    const publish = (outcomes: TargetOutcome[], running: boolean) => {
      if (closingTabs.current.has(tabId) || !mounted.current) return;
      setMultiRuns((current) => ({ ...current, [tabId]: { outcomes, statements: statements.length, running } }));
    };
    setBottomTab("connections");
    let outcomes = initialOutcomes(targets, statements.length);
    publish(outcomes, true);
    try {
      await runOnConnections(
        { targets: targets.map((target) => ({ connectionId: target.connectionId, database: target.database })), statements, concurrency, backup },
        controller.signal,
        (event) => {
          outcomes = applyEvent(outcomes, event);
          publish(outcomes, true);
        },
      );
      publish(outcomes, false);
    } catch (err) {
      outcomes = controller.signal.aborted ? stopOutcomes(outcomes) : failOutcomes(outcomes, err instanceof Error ? err.message : "The run could not continue.");
      publish(outcomes, false);
    } finally {
      if (multiControllers.current[tabId] === controller) delete multiControllers.current[tabId];
      const changed = scriptChanges(statements);
      for (const target of targets) {
        void queryClient.invalidateQueries({ queryKey: ["history", target.connectionId] });
        if (changed) void queryClient.invalidateQueries({ queryKey: ["schema", target.connectionId] });
      }
    }
  }

  function onRunOnConnections() {
    if (isMongo) return;
    const selected = selectedSql.trim();
    const sql = selected || currentSql;
    if (!sql.trim()) {
      setActiveMessage("Nothing to run: the editor is empty.", true);
      return;
    }
    if (parameterNames(sql, activeConnection?.engine ?? "").length) { setActiveMessage("Run parameterized queries on one connection at a time.", true); setBottomTab("messages"); return; }
    setMultiRunDialog({ sql, source: selected ? "selection" : "editor" });
  }

  function onRunAll() {
    if (isMongo || isRedis || isElasticsearch) { void execute(currentSql); return; }
    void runStatements(splitStatements(currentSql, activeConnection?.engine));
  }

  // Statements run one by one, each keeping its own result; the first error
  // or a pending statement stops the rest. Not atomic unless in manual commit.
  async function runStatements(statements: { sql: string }[]) {
    const tabId = activeTabId;
    if (!statements.length || scripts.current[tabId] || controllers.current[tabId] || transactionOperations.current.has(tabId) || closingTabs.current.has(tabId)) return;
    // Validate every parameter before any statement can make a change.
    try { statements.forEach(statement => resolveParameters(statement.sql, activeConnection?.engine ?? "", parameters)); }
    catch (error) { setActiveMessage((error as Error).message, true); setBottomTab("messages"); return; }
    scripts.current[tabId] = true;
    const results: StatementResult[] = [];
    patchRun(tabId, { results: [], activeResult: 0 });
    try {
      for (const statement of statements) {
        if (!scripts.current[tabId]) break;
        delete lastOutcome.current[tabId];
        const ok = await execute(statement.sql, true);
        const outcome = lastOutcome.current[tabId];
        if (!outcome || !mounted.current) break;
        results.push({ sql: statement.sql, ...outcome });
        patchRun(tabId, { results: [...results], activeResult: results.length - 1 });
        if (!ok) break;
      }
    } finally { delete scripts.current[tabId]; }
    if (!mounted.current || results.length === 0) return;
    const succeeded = results.filter(result => result.status === "success").length;
    const last = results[results.length - 1];
    const summary = `${succeeded} of ${statements.length} statement(s) succeeded${results.length < statements.length ? `; stopped at statement ${results.length}` : ""}.`;
    patchRun(tabId, { message: last.status === "success" ? summary : `${summary}\n${last.message}`, messageError: last.status === "error" });
  }

  function setCommitMode(manual: boolean) {
    const tabId = activeTabId;
    if (transactions.current[tabId] || controllers.current[tabId] || scripts.current[tabId] || transactionOperations.current.has(tabId)) return;
    manualCommit.current = { ...manualCommit.current, [tabId]: manual };
    setManualCommitTabs(manualCommit.current);
    setActiveMessage(manual ? "Manual commit: the next statement starts a transaction. Nothing is saved until you press Commit." : "Auto-commit: each statement is saved as soon as it succeeds.", false);
  }

  async function transactionAction(action: "begin" | "commit" | "rollback") {
    if (!activeTabId || !activeConnectionId || transactionOperations.current.has(activeTabId) || closingTabs.current.has(activeTabId) || scripts.current[activeTabId] || controllers.current[activeTabId]) return;
    transactionOperations.current.add(activeTabId);
    setTransactionBusy(true);
    try {
      const tx = transactions.current[activeTabId];
      if (action === "begin" && !tx) {
        const result = await beginTxn(activeConnectionId, selectedDb);
        if (!mounted.current) {
          await rollbackTxn(activeConnectionId, result.txnId);
          return;
        }
        transactions.current[activeTabId] = { id: result.txnId, connectionId: activeConnectionId, database: selectedDb };
        setTransactionIDs(current => ({ ...current, [activeTabId]: result.txnId }));
      } else if (tx && action !== "begin") {
        await (action === "commit" ? commitTxn(tx.connectionId, tx.id) : rollbackTxn(tx.connectionId, tx.id));
        forgetTransaction(activeTabId);
      }
      setActiveMessage(action === "begin" ? "Transaction open. Changes require Commit; close or Rollback to discard." : `Transaction ${action} completed.${manualCommit.current[activeTabId] ? " Still in manual commit; the next statement starts a new transaction." : ""}`, false);
    } catch (error) {
      if (error instanceof ApiError && ["TXN_NOT_FOUND", "TXN_FINISH_ERROR", "TXN_LOST", "TXN_ROLLED_BACK"].includes(error.body.code)) forgetTransaction(activeTabId);
      if (mounted.current) setActiveMessage(error instanceof Error ? error.message : "Transaction failed", true);
    }
    finally { transactionOperations.current.delete(activeTabId); if (mounted.current) setTransactionBusy(transactionOperations.current.size > 0); }
  }

  function onDatabaseChange(db: string) {
    if (transactions.current[activeTabId] || controllers.current[activeTabId] || scripts.current[activeTabId] || transactionOperations.current.has(activeTabId) || closingTabs.current.has(activeTabId)) return;
    if (!activeTab) return;
    setTabs((current) => current.map((tab) => tab.id === activeTab.id ? { ...tab, database: db } : tab));
  }

  function onNodeRoleChange(role: "primary" | "secondary") {
    if (transactions.current[activeTabId] || controllers.current[activeTabId] || scripts.current[activeTabId] || transactionOperations.current.has(activeTabId) || closingTabs.current.has(activeTabId)) return;
    if (!activeTab) return;
    setTabs((current) => current.map((tab) => tab.id === activeTab.id ? { ...tab, nodeRole: role } : tab));
  }

  function onConnectionChange(id: string | null) {
    if (transactions.current[activeTabId] || controllers.current[activeTabId] || scripts.current[activeTabId] || transactionOperations.current.has(activeTabId) || closingTabs.current.has(activeTabId)) return;
    if (!activeTab) return;
    const connection = connections.find((item) => item.id === id);
    setTabs((current) => current.map((tab) => tab.id === activeTab.id ? {
      ...tab,
      connectionId: id,
      sql: !tab.sql.trim() ? defaultSql(connection?.engine) : tab.sql,
      database: connection?.database || defaultDatabase(connection?.engine),
      nodeRole: connection?.defaultNodeRole ?? "primary",
    } : tab));
    setActiveConnection(id);
  }

  // Rows of a one-table result can be edited; the edits run as UPDATEs through
  // runStatements, followed by the original query to reload the result.
  const rowEditing: RowEditing | undefined = activeConnection && !activeConnection.readOnly && selectedNodeRole !== "secondary" && !["mongodb", "redis", "valkey", "elasticsearch"].includes(activeConnection.engine) ? {
    engine: activeConnection.engine,
    primaryKey: (schemaName, table) => {
      const nodes = schema?.schemas ?? [];
      const node = nodes.find((item) => item.name.toLowerCase() === schemaName.toLowerCase()) ?? (nodes.length === 1 ? nodes[0] : undefined);
      const found = node?.tables.find((item) => item.name.toLowerCase() === table.toLowerCase());
      const key = found?.columns.filter((column) => column.pk).map((column) => column.name) ?? [];
      return key.length ? key : null;
    },
    onApply: (statements, sourceSql) => void runStatements([...statements.map((sql) => ({ sql })), { sql: sourceSql }]),
  } : undefined;

  const denialContext: DenialContext | undefined = activeConnectionId ? {
    connectionId: activeConnectionId,
    sql: selectedSql.trim() || currentSql,
    onMessage: (message, isError) => {
      if (isError) setActiveMessage(message, true);
      else patchRun(activeTabId, { status: "idle", data: undefined, error: undefined, message, messageError: false });
      setBottomTab("messages");
    },
  } : undefined;

  return (
    <SchemaActions.Provider value={(action) => {
      if (action.append && activeConnectionId === action.connectionId && selectedDb === action.database) {
        updateActiveSql(currentSql + action.sql);
      } else {
        const id = crypto.randomUUID();
        setTabs(docs => [...docs, { id, title: "Table query", sql: action.sql, connectionId: action.connectionId, database: action.database }]);
        setActiveTabId(id);
      }
    }}>
    <div
      className={`grid h-[calc(100vh-1.5rem)] min-h-[620px] ${explorerOpen ? "gap-x-0 lg:grid-cols-[var(--explorer-width)_8px_minmax(0,1fr)]" : "gap-2 lg:grid-cols-[34px_minmax(0,1fr)]"}`}
      style={{ "--explorer-width": `${explorerWidth}px` } as CSSProperties}
    >
      {explorerOpen ? (
        <ExplorerPanel
          connections={connections}
          activeConnectionId={activeConnectionId}
          selectedDb={selectedDb}
          onSelectConnection={onConnectionChange}
          onSelectDatabase={onDatabaseChange}
          onCollapse={() => setExplorerOpen(false)}
        />
      ) : (
        <button
          onClick={() => setExplorerOpen(true)}
          className="hidden h-full flex-col items-center gap-2 rounded-lg border border-slate-200 bg-white pt-3 text-[11px] font-semibold tracking-wide text-slate-500 hover:bg-slate-50 hover:text-slate-800 dark:border-slate-800 dark:bg-slate-950 dark:hover:bg-slate-900 lg:flex"
          title="Open explorer"
        >
          <Icon name="panel-left" size={16} className="text-slate-400" />
          <span className="[writing-mode:vertical-rl]">Explorer</span>
        </button>
      )}

      {explorerOpen && (
        <ColumnResizeHandle
          onResize={(delta) => setExplorerWidth((width) => Math.min(560, Math.max(220, width + delta)))}
        />
      )}

      <Panel className={`flex min-w-0 flex-col overflow-hidden ${activeConnection ? envFrame[envKind(activeConnection.environment)] : ""}`}>
        {/* Routine save state lives in the toolbar; only problems get a banner. */}
        {(persistence.error || persistence.storageError) && (
          <div role="alert" className="flex flex-wrap items-center gap-3 bg-amber-50 px-3 py-1 text-xs text-amber-800 dark:bg-amber-950 dark:text-amber-200">
            {persistence.error && <span>{persistence.error}</span>}
            {persistence.storageError && <span>Local recovery storage unavailable. Export drafts before closing.</span>}
            {persistence.error && <button className="underline" onClick={persistence.retry}>Retry save</button>}
            <button className="underline" onClick={() => exportWorkspace(workspace)}>Export workspace</button>
          </div>
        )}
        <input ref={workspaceFileInput} type="file" accept=".json,application/json" className="hidden" aria-label="Import workspace" onChange={event => { void importWorkspace(event.target.files?.[0]); event.target.value = ""; }} />
        {activeConnection && <div className={`h-1 w-full ${envRail[envKind(activeConnection.environment)]}`} />}
        <WorkspaceTabs
          tabs={tabs}
          activeTabId={activeTabId}
          onSelect={setActiveTabId}
          onAdd={addTab}
          onClose={closeTab}
          runStates={runStates}
          workspaceStatus={persistence.error
            ? { label: "Tabs not saved", tone: "warn", detail: persistence.error }
            : persistence.dirty
              ? { label: "Saving tabs…", tone: "busy", detail: "Your open tabs are being saved automatically." }
              : { label: "Tabs auto-saved", tone: "ok", detail: "Your open tabs (SQL text, connection and database) are saved automatically and encrypted, and reopen next time. Query results and open transactions are not kept.\nUse Save to keep a query in your saved queries." }}
        />

        {leaveBlocker.state === "blocked" && (
          <Modal title="Leave the SQL editor?" onClose={() => leaveBlocker.reset?.()}>
            {runningTabs > 0 && <p className="text-[13px] text-slate-600 dark:text-slate-300">{runningTabs === 1 ? "A query is" : `${runningTabs} queries are`} still running. Leaving stops {runningTabs === 1 ? "it" : "them"}; a write that already reached the database may still be applied.</p>}
            {openTransactions > 0 && <p className="mt-2 text-[13px] text-slate-600 dark:text-slate-300">{openTransactions === 1 ? "A tab has" : `${openTransactions} tabs have`} an open transaction. Leaving rolls back changes that were not committed.</p>}
            {runningTabs === 0 && openTransactions === 0 && <p className="text-[13px] text-slate-600 dark:text-slate-300">Go back to the previous page? Your tabs are saved and will be here when you return.</p>}
            <div className="mt-4 flex justify-end gap-2">
              <button type="button" onClick={() => leaveBlocker.proceed?.()} className="h-8 rounded-md px-3 text-[13px] text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800">{leaveByBack.current ? "Go back" : "Leave"}</button>
              <Button onClick={() => leaveBlocker.reset?.()}>Stay in the editor</Button>
            </div>
          </Modal>
        )}
        {backupPrompt && backupPrompt.tabId === activeTabId && (
          <Modal title="Run without a backup?" onClose={() => setBackupPrompt(null)}>
            <p className="text-[13px] text-slate-600 dark:text-slate-300">{backupPrompt.reason} The statement has not run.</p>
            <p className="mt-2 text-[13px] text-slate-600 dark:text-slate-300">If you run it anyway, the rows it changes cannot be restored from Row backups.</p>
            <div className="mt-4 flex justify-end gap-2">
              <button type="button" onClick={() => setBackupPrompt(null)} className="h-8 rounded-md px-3 text-[13px] text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800">Cancel</button>
              <Button onClick={() => { const prompt = backupPrompt; setBackupPrompt(null); void execute(prompt.sql, false, true); }}>Run without backup</Button>
            </div>
          </Modal>
        )}
        <RunToolbar
          connectionId={activeConnectionId}
          onConnectionChange={onConnectionChange}
          onRun={onRun}
          onOpenFile={() => fileInput.current?.click()}
          onSaveFile={saveSQLFile}
          onExportWorkspace={() => exportWorkspace(workspace)}
          onImportWorkspace={() => workspaceFileInput.current?.click()}
          onRunAll={onRunAll}
          onRunOnConnections={activeConnection && !["mongodb", "redis", "valkey", "elasticsearch"].includes(activeConnection.engine) ? onRunOnConnections : undefined}
          onExplain={(analyze) => void onExplain(analyze)}
          onSchedule={!shared && activeConnection && !["mongodb", "redis", "valkey", "cassandra", "elasticsearch"].includes(activeConnection.engine) && !parameterList.length ? () => navigate("/schedules", { state: { newSchedule: { sql: statementUnderCursor(), connectionId: activeConnectionId, database: selectedDb } } }) : undefined}
          onStop={() => {
            if (transactions.current[activeTabId] && !window.confirm("Stopping a statement inside a transaction ends the transaction and discards its uncommitted changes. Stop anyway?")) return;
            scripts.current[activeTabId] = false; controllers.current[activeTabId]?.abort();
          }}
          manualCommit={Boolean(manualCommitTabs[activeTabId])}
          onManualCommitChange={setCommitMode}
          pendingStatements={pendingStatements[activeTabId] ?? 0}
          transactionOpen={Boolean(transactionIDs[activeTabId])}
          transactionAborted={Boolean(abortedTransactions[activeTabId])}
          transactionBusy={transactionBusy}
          onTransaction={(action) => void transactionAction(action)}
          assistantOpen={assistantOpen}
          onAssistantToggle={isMongo ? undefined : () => setAssistantOpen((open) => !open)}
          onFormat={formatCurrent}
          onSave={saveCurrent}
          running={activeRun.status === "running"}
          selectionStatements={selectionStatements}
          database={selectedDb}
          onDatabaseChange={onDatabaseChange}
          nodeRole={selectedNodeRole}
          onNodeRoleChange={onNodeRoleChange}
          snippetsMenu={<SnippetsMenu connectionId={activeConnectionId} database={selectedDb} sql={selectedSql.trim() || currentSql} />}
          autoRefreshMs={autoRefreshMs}
          onAutoRefreshChange={(ms) => setAutoRefreshTabs((current) => ({ ...current, [activeTabId]: ms }))}
          autoRefreshEligible={autoRefreshEligible}
        />

        {isRedis && <RedisQueryBar tabKey={activeTabId} sql={currentSql} onChange={updateActiveSql} onRun={onRun} />}
        {isElasticsearch && <ElasticsearchQueryBar tabKey={activeTabId} sql={currentSql} onChange={updateActiveSql} onRun={onRun} />}
        {isMongo && <MongoQueryBar tabKey={activeTabId} sql={currentSql} onChange={updateActiveSql} onRun={onRun} />}
        <QueryParameters names={parameterList} values={parameters} onChange={values => setTabParameters(current => ({ ...current, [activeTabId]: values }))} />
        <div className="flex min-h-0 flex-1">
        <div className="flex min-h-0 min-w-0 flex-1 flex-col">
          <input ref={fileInput} type="file" accept=".sql,.cql,.json,text/plain,application/sql,application/json" className="hidden" aria-label="Open query file" onChange={(event) => { void openSQLFile(event.target.files?.[0]); event.target.value = ""; }} />
          <div className="flex min-h-[120px] flex-1 flex-col border-b border-slate-200 dark:border-slate-800">
            <Suspense
              fallback={<div className="grid h-full place-items-center text-xs text-slate-500">Loading editor...</div>}
            >
              <MonacoSqlEditor
                key={`${activeTabId}:${isMongo || isRedis || isElasticsearch}`}
                language={isMongo || isRedis || isElasticsearch ? "json" : "sql"}
                value={currentSql}
                onChange={updateActiveSql}
                onSelectionChange={setSelectedSql}
                onCursorChange={setCursor}
                onRun={onRun}
                onRunAll={onRunAll}
                completions={completions}
              />
            </Suspense>
            <div className="flex h-6 items-center justify-between border-t border-slate-200 bg-[#f6f7f9] px-3 text-[11px] text-slate-500 dark:border-slate-800 dark:bg-slate-950">
              <span>
                Ln {cursor.line}, Col {cursor.column}
                {selectedSql ? ` · ${selectedSql.length} selected` : ""}
              </span>
              <span>{currentSql.length} chars · UTF-8 · {isMongo ? "MongoDB · Extended JSON" : isRedis ? `${activeConnection?.engine === "valkey" ? "Valkey" : "Redis"} · JSON` : isElasticsearch ? "Elasticsearch · JSON" : isCassandra ? "CQL" : "SQL"}</span>
            </div>
          </div>

          <ResizeHandle onResize={(delta) => setBottomHeight((h) => Math.min(720, Math.max(120, h + delta)))} />

          <div className="flex flex-col overflow-hidden" style={{ height: bottomHeight }}>
          <BottomPanel
              key={activeTabId}
              activeTab={bottomTab}
              onChange={setBottomTab}
              run={activeRun}
              connectionId={activeConnectionId}
              engine={activeConnection?.engine}
              onPickHistory={(sql) => {
                updateActiveSql(sql);
                setBottomTab("results");
              }}
              denialContext={denialContext}
              plan={plans[activeTabId]}
              multiRun={multiRuns[activeTabId]}
              onStopMultiRun={() => multiControllers.current[activeTabId]?.abort()}
              rowEditing={rowEditing}
              onSelectResult={(index) => patchRun(activeTabId, { activeResult: index })}
              onExportAllRows={capabilities.csvExport && activeRun.sql && activeConnectionId && !transactionIDs[activeTabId] ? () => exportTable(activeConnectionId, { database: selectedDb || undefined, sql: activeRun.sql!, format: "csv" }).then((blob) => {
                const url = URL.createObjectURL(blob);
                const link = document.createElement("a");
                link.href = url;
                link.download = "query-result.csv";
                link.click();
                window.setTimeout(() => URL.revokeObjectURL(url), 1000);
              }) : undefined}
            />
          </div>
        </div>
        {assistantOpen && !isMongo && (
          <aside className="flex w-[360px] shrink-0 flex-col border-l border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-950">
            <div className="flex h-8 shrink-0 items-center gap-2 border-b border-slate-200 px-2 text-xs dark:border-slate-800">
              <Icon name="wand" size={13} className="text-slate-400" />
              <span className="font-medium text-slate-700 dark:text-slate-200">Assistant</span>
              <button type="button" onClick={() => setAssistantOpen(false)} title="Close the assistant" aria-label="Close the assistant" className="ml-auto grid h-6 w-6 place-items-center rounded text-slate-400 hover:bg-slate-100 hover:text-slate-700 dark:hover:bg-slate-900 dark:hover:text-slate-200">
                <Icon name="close" size={13} />
              </button>
            </div>
            <div className="min-h-0 flex-1 overflow-auto">
              <AssistantPanel
                sql={(selectedSql.trim() || currentSql).trim()}
                connectionId={activeConnectionId}
                database={selectedDb || undefined}
                error={activeRun.messageError ? activeRun.message : undefined}
                plan={plans[activeTabId]?.result?.plan}
                onInsert={(sql) => openSqlTab(sql, "From assistant")}
              />
            </div>
          </aside>
        )}
        </div>
      </Panel>
    </div>
    {multiRunDialog && (
      <MultiRunDialog
        sql={multiRunDialog.sql}
        source={multiRunDialog.source}
        connections={connections}
        initialIds={activeConnectionId ? [activeConnectionId] : []}
        onClose={() => setMultiRunDialog(null)}
        onRun={(targets, statements, concurrency) => void startMultiRun(targets, statements, concurrency)}
      />
    )}
    {saveDialogOpen && (
      <SaveToNotebookDialog
        sql={selectedSql.trim() || currentSql}
        connectionId={activeConnectionId}
        database={selectedDb}
        defaultTitle={activeTab?.title ?? ""}
        onClose={() => setSaveDialogOpen(false)}
        onSaved={(title) => setActiveMessage(`Saved to notebook “${title}”.`, false)}
      />
    )}
    </SchemaActions.Provider>
  );
}

function ResizeHandle({ onResize }: { onResize: (delta: number) => void }) {
  const lastY = useRef(0);
  function onPointerDown(e: React.PointerEvent) {
    e.preventDefault();
    lastY.current = e.clientY;
    document.body.style.userSelect = "none";
    document.body.style.cursor = "row-resize";
    const move = (ev: PointerEvent) => {
      onResize(lastY.current - ev.clientY);
      lastY.current = ev.clientY;
    };
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      document.body.style.userSelect = "";
      document.body.style.cursor = "";
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  }
  return (
    <div
      onPointerDown={onPointerDown}
      className="group relative flex h-1.5 shrink-0 cursor-row-resize items-center justify-center bg-slate-100 transition hover:bg-cyan-100 dark:bg-slate-900 dark:hover:bg-cyan-500/20"
      title="Drag to resize results"
    >
      <span className="h-0.5 w-8 rounded-full bg-slate-300 transition group-hover:bg-cyan-500 dark:bg-slate-700" />
    </div>
  );
}

function ColumnResizeHandle({ onResize }: { onResize: (delta: number) => void }) {
  const lastX = useRef(0);
  function onPointerDown(e: React.PointerEvent) {
    e.preventDefault();
    lastX.current = e.clientX;
    document.body.style.userSelect = "none";
    document.body.style.cursor = "col-resize";
    const move = (ev: PointerEvent) => {
      onResize(ev.clientX - lastX.current);
      lastX.current = ev.clientX;
    };
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      document.body.style.userSelect = "";
      document.body.style.cursor = "";
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  }
  return (
    <div
      onPointerDown={onPointerDown}
      className="group relative hidden cursor-col-resize items-center justify-center bg-transparent lg:flex"
      title="Drag to resize explorer"
    >
      <span className="h-full w-px rounded-full bg-slate-200 transition group-hover:w-1 group-hover:bg-cyan-400 dark:bg-slate-800" />
    </div>
  );
}

function WorkspaceTabs({
  tabs,
  activeTabId,
  onSelect,
  onAdd,
  onClose,
  runStates,
  workspaceStatus,
}: {
  tabs: QueryTab[];
  activeTabId: string;
  onSelect: (id: string) => void;
  onAdd: () => void;
  onClose: (id: string) => void;
  runStates: Record<string, TabRunState>;
  workspaceStatus: WorkspaceStatus;
}) {
  return (
    <div className="flex h-9 items-center justify-between gap-2 border-b border-slate-200 bg-slate-50 px-1.5 dark:border-slate-800 dark:bg-slate-950">
      <div className="flex min-w-0 items-center gap-1 overflow-x-auto">
        {tabs.map((tab) => {
          const active = activeTabId === tab.id;
          const running = runStates[tab.id]?.status === "running";
          return (
            <div
              key={tab.id}
              onClick={() => onSelect(tab.id)}
              className={`group flex h-7 shrink-0 cursor-pointer items-center gap-1.5 rounded-md border px-2 text-[13px] transition ${
                active
                  ? "border-slate-200 bg-white text-slate-900 shadow-sm dark:border-cyan-400/20 dark:bg-cyan-500/10 dark:text-slate-50"
                  : "border-transparent text-slate-500 hover:bg-white/70 dark:text-slate-400 dark:hover:bg-slate-900"
              }`}
            >
              {running ? (
                <span
                  className="inline-flex h-2.5 w-2.5 shrink-0 animate-pulse rounded-full bg-amber-500"
                  title="Query running"
                />
              ) : (
                <Icon name="sql" size={13} className={active ? "text-cyan-600 dark:text-cyan-300" : "text-slate-400"} />
              )}
              <span className="max-w-[140px] truncate">{tab.title}</span>
              <button
                onClick={(e) => {
                  e.stopPropagation();
                  onClose(tab.id);
                }}
                className={`grid h-4 w-4 place-items-center rounded text-slate-400 transition hover:bg-slate-200 hover:text-slate-700 dark:hover:bg-slate-700 dark:hover:text-slate-100 ${
                  active ? "opacity-100" : "opacity-0 group-hover:opacity-100"
                }`}
                title="Close tab"
              >
                <Icon name="close" size={11} />
              </button>
            </div>
          );
        })}
      </div>
      <span
        className={`ml-auto inline-flex shrink-0 items-center gap-1 px-1 text-[11px] ${workspaceStatus.tone === "warn" ? "text-amber-600" : "text-slate-400"}`}
        title={workspaceStatus.detail}
      >
        <Icon name={workspaceStatus.tone === "warn" ? "alert" : workspaceStatus.tone === "busy" ? "refresh" : "check"} size={12} className={workspaceStatus.tone === "busy" ? "animate-spin" : ""} />
        {workspaceStatus.label}
      </span>
      <button
        onClick={onAdd}
        className="flex h-7 shrink-0 items-center gap-1.5 rounded-md border border-slate-200 bg-white px-2 text-[12px] font-medium text-slate-600 transition hover:bg-slate-50 hover:text-slate-900 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800"
        title="New query tab"
      >
        <Icon name="plus" size={13} />
        New Query
      </button>
    </div>
  );
}

function BottomPanel({
  activeTab,
  onChange,
  run,
  connectionId,
  engine,
  onPickHistory,
  denialContext,
  plan,
  multiRun,
  onStopMultiRun,
  rowEditing,
  onSelectResult,
  onExportAllRows,
}: {
  activeTab: BottomTab;
  onChange: (tab: BottomTab) => void;
  run: TabRunState;
  connectionId: string | null;
  /** The tab's connection engine, so results can pick a sensible default view. */
  engine?: string;
  onPickHistory: (sql: string) => void;
  denialContext?: DenialContext;
  plan?: PlanState;
  /** The last run of a script on several connections in this tab. */
  multiRun?: MultiRunState;
  onStopMultiRun?: () => void;
  rowEditing?: RowEditing;
  onSelectResult?: (index: number) => void;
  /** Downloads every row of the statement's result. */
  onExportAllRows?: () => Promise<void>;
}) {
  // Several statements ran: show one result at a time, picked from a strip.
  const results = run.results && run.results.length > 1 ? run.results : undefined;
  // Reported by ResultsGrid's own filter state, so the status bar can show
  // how many of the loaded rows a filter left visible.
  const [filteredCount, setFilteredCount] = useState<{ shown: number; total: number } | null>(null);
  const activeResult = results ? Math.min(run.activeResult ?? results.length - 1, results.length - 1) : -1;
  const selected = results && run.status !== "running" ? results[activeResult] : undefined;
  const shownRun: TabRunState = selected ? { ...run, status: selected.status, data: selected.data, error: selected.error } : run;
  // The plan tab appears once Explain has produced one, and goes away with
  // the next run.
  const visibleTab: BottomTab = (activeTab === "plan" && !plan) || (activeTab === "connections" && !multiRun) ? "results" : activeTab;
  const tabMeta: Record<BottomTab, { label: string; icon: IconName }> = {
    results: { label: "Results", icon: "grid" },
    messages: { label: "Messages", icon: "text" },
    history: { label: "History", icon: "history" },
    plan: { label: "Plan", icon: "explain" },
    connections: { label: "Connections", icon: "database" },
  };
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-8 items-center gap-1 border-b border-slate-200 bg-slate-50 px-2 text-xs dark:border-slate-800 dark:bg-slate-950">
        {(["results", "messages", "history", "plan", "connections"] as BottomTab[]).filter((tab) => (tab !== "plan" || plan) && (tab !== "connections" || multiRun)).map((tab) => (
          <button
            key={tab}
            onClick={() => onChange(tab)}
            className={`flex h-8 items-center gap-1.5 border-b-2 px-2 transition ${
              visibleTab === tab
                ? "border-slate-900 text-slate-900 dark:border-cyan-500 dark:text-slate-100"
                : "border-transparent text-slate-500 hover:text-slate-900 dark:text-slate-400 dark:hover:text-slate-200"
            }`}
          >
            <Icon name={tabMeta[tab].icon} size={13} />
            {tabMeta[tab].label}
          </button>
        ))}
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        {visibleTab === "results" && (
          <>
            {results && (
              <div role="tablist" aria-label="Statement results" className="flex gap-1 overflow-x-auto border-b border-slate-200 px-2 py-1 dark:border-slate-800">
                {results.map((result, index) => (
                  <button
                    key={index}
                    role="tab"
                    aria-selected={index === activeResult}
                    title={`${result.data ? `${result.data.rowCount} rows` : result.status === "pending" ? "Pending" : "Failed"}\n\n${result.sql}`}
                    onClick={() => onSelectResult?.(index)}
                    className={`inline-flex shrink-0 items-center gap-1.5 rounded px-2 py-0.5 text-[11px] ${index === activeResult ? "bg-slate-200 text-slate-900 dark:bg-slate-800 dark:text-slate-100" : "text-slate-500 hover:bg-slate-100 dark:hover:bg-slate-900"}`}
                  >
                    <span className={`h-1.5 w-1.5 rounded-full ${result.status === "success" ? "bg-emerald-500" : result.status === "pending" ? "bg-amber-500" : "bg-rose-500"}`} />
                    Result {index + 1}
                  </button>
                ))}
              </div>
            )}
            {(shownRun.status === "error" || shownRun.status === "pending") && shownRun.error ? (
              <PolicyBanner error={shownRun.error} context={denialContext} />
            ) : shownRun.data ? (
              <ResultsGrid key={`${run.startedAt}:${activeResult}`} result={shownRun.data} editing={editingFor(rowEditing, selected ? selected.sql : run.sql)} engine={engine} onFilteredCount={(shown, total) => setFilteredCount({ shown, total })} />
            ) : (
              run.status !== "running" && (
                <EmptyState title="No results yet" text="Run a query to populate the result grid." />
              )
            )}
          </>
        )}
        {visibleTab === "history" && <HistoryPanel connectionId={connectionId} onPick={onPickHistory} />}
        {visibleTab === "plan" && <PlanPanel plan={plan} />}
        {visibleTab === "connections" && multiRun && <MultiRunPanel state={multiRun} onStop={() => onStopMultiRun?.()} />}
        {visibleTab === "messages" && (
          <MessagePanel message={run.message} error={run.messageError ? run.message : ""} />
        )}
      </div>
      <StatusBar run={shownRun} onExportAllRows={results ? undefined : onExportAllRows} filteredCount={filteredCount} />
    </div>
  );
}

function StatusBar({ run, onExportAllRows, filteredCount }: { run: TabRunState; onExportAllRows?: () => Promise<void>; filteredCount?: { shown: number; total: number } | null }) {
  const [exporting, setExporting] = useState("");
  const badges = useActiveExtensions().flatMap((item) => item.resultBadges ?? []);
  const [now, setNow] = useState(() => Date.now());
  const running = run.status === "running";
  const runStartedAt = run.startedAt ?? null;
  const runEndedAt = run.endedAt ?? null;

  useEffect(() => {
    if (!running || !runStartedAt) return;
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 200);
    return () => window.clearInterval(timer);
  }, [running, runStartedAt]);

  const result = run.data;
  const elapsedMs =
    runStartedAt && (running || result || run.status === "error")
      ? Math.max(0, (runEndedAt ?? now) - runStartedAt)
      : 0;
  const elapsed = elapsedMs ? `${(elapsedMs / 1000).toFixed(elapsedMs < 10000 ? 1 : 0)}s` : "";
  const ready = !!result;
  const statusLabel = running ? "Running" : run.status === "pending" ? "Pending" : run.status === "error" ? "Failed" : ready ? "Completed" : "Ready";
  // Only trust it once it's actually about this result, not a stale value
  // left over from the previous one.
  const filtered = result && filteredCount && filteredCount.total === result.rowCount && filteredCount.shown !== filteredCount.total ? filteredCount : null;
  const rowLabel = result ? `${result.rowCount} rows` : "";
  const rowsetLabel = result ? `rowset ${result.durationMs}ms` : "";
  const studioLabel = elapsed ? `studio ${elapsed}` : "";
  const rawNode = result?.annotations?.node;
  const nodeAnnotation = isNodeAnnotation(rawNode) ? rawNode : null;

  return (
    <div className="flex h-9 items-center gap-2 border-t border-slate-200 bg-white px-3 text-[11px] text-slate-500 dark:border-slate-800 dark:bg-slate-950">
      <span
        className={`inline-flex h-2.5 w-2.5 shrink-0 rounded-full ${
          running || run.status === "pending" ? "bg-amber-500 shadow-[0_0_0_3px_rgba(245,158,11,0.15)]" : run.status === "error" ? "bg-rose-500 shadow-[0_0_0_3px_rgba(244,63,94,0.15)]" : "bg-emerald-500 shadow-[0_0_0_3px_rgba(16,185,129,0.15)]"
        }`}
        title={statusLabel}
      />
      <span className="font-medium text-slate-600 dark:text-slate-300">{statusLabel}</span>
      <div className="flex min-w-0 flex-1 items-center gap-2">
        {result && badges.map((Badge, index) => <Badge key={index} result={result} />)}
        {nodeAnnotation && (
          <span
            className="inline-flex shrink-0 items-center gap-1 rounded border border-slate-200 bg-slate-50 px-1.5 py-0.5 font-medium text-slate-600 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300"
            title={`This ran against ${nodeAnnotation.host}${nodeAnnotation.role ? ` (${nodeAnnotation.role})` : ""}`}
          >
            <Icon name="database" size={11} />
            {nodeAnnotation.host}
          </span>
        )}
        {result && result.policyNotice && (
          // A policy capped the result; the rows behind it can still be
          // exported when no policy forbids that.
          <span className="inline-flex min-w-0 items-center gap-2">
            <span className="inline-flex shrink-0 items-center gap-1 rounded border border-sky-200 bg-sky-50 px-1.5 py-0.5 font-medium text-sky-700 dark:border-sky-500/30 dark:bg-sky-500/10 dark:text-sky-300" title={result.policyNotice}>
              <Icon name="shield" size={11} />
              {result.policyNotice}
            </span>
            {onExportAllRows && (
              <button
                type="button"
                disabled={exporting === "running"}
                onClick={() => {
                  setExporting("running");
                  onExportAllRows().then(() => setExporting(""), (err: unknown) => setExporting(err instanceof Error ? err.message : "Export failed"));
                }}
                title={exporting && exporting !== "running" ? exporting : "Download every row of this result as CSV"}
                className={`shrink-0 font-medium hover:underline ${exporting && exporting !== "running" ? "text-rose-600" : "text-sky-700 dark:text-sky-300"}`}
              >
                {exporting === "running" ? "Exporting…" : exporting ? "Export failed" : "Export all rows (CSV)"}
              </button>
            )}
          </span>
        )}
      </div>
      <div className="ml-auto flex min-w-[13rem] items-center justify-end gap-1.5 text-right font-mono tabular-nums text-slate-500 dark:text-slate-400">
        {ready && filtered && (
          <span className="rounded bg-sky-100 px-1.5 py-0.5 font-sans font-medium text-sky-800 dark:bg-sky-500/15 dark:text-sky-300">
            {filtered.shown} of {filtered.total} rows filtered
          </span>
        )}
        <span>
          {ready
            ? [filtered ? null : rowLabel, rowsetLabel, studioLabel].filter(Boolean).join(" · ")
            : studioLabel || " "}
        </span>
      </div>
    </div>
  );
}

function EmptyState({ title, text }: { title: string; text: string }) {
  return (
    <div className="grid min-h-64 place-items-center px-6 py-12">
      <div className="max-w-md text-center">
        <div className="text-sm font-medium text-slate-900 dark:text-slate-100">{title}</div>
        <div className="mt-2 text-sm leading-6 text-slate-500 dark:text-slate-400">{text}</div>
      </div>
    </div>
  );
}

function MessagePanel({ message, error }: { message: string; error: string }) {
  return (
    <div className="p-3 font-mono text-[12px] leading-6">
      {error ? (
        <div className="flex items-start gap-2 text-rose-600 dark:text-rose-400">
          <Icon name="alert" size={14} className="mt-1" />
          <span className="whitespace-pre-wrap">{error}</span>
        </div>
      ) : (
        <div className="flex items-start gap-2 text-slate-600 dark:text-slate-300">
          <Icon name="check" size={14} className="mt-1 text-emerald-500" />
          <span>{message || "Ready."}</span>
        </div>
      )}
    </div>
  );
}

function defaultSql(engine?: string) {
  if (engine === "mongodb") return mongoQuery();
  if (engine === "redis" || engine === "valkey") return redisQuery();
  if (engine === "elasticsearch") return elasticsearchQuery();
  return "";
}

function defaultDatabase(engine?: string) {
  if (engine === "mysql" || engine === "mariadb") return "mysql";
  if (engine === "sqlserver" || engine === "mssql") return "master";
  return "postgres";
}
