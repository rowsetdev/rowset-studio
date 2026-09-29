import { useEffect, useRef, useState, type ReactNode } from "react";
import { ApiError, api } from "../../lib/api";
import { useAuth } from "../../lib/auth";
import { Button, Panel } from "../../components/ui";
import { validWorkspace, mergeWorkspace, missingTabs, WorkspaceWriter, type WorkspaceDocument, type WorkspaceSnapshot } from "./workspace";

function userKey() {
  const user = useAuth.getState().user;
  return `${user?.orgId}:${user?.userId}`;
}
function recoveryPrefix() { return `rowset.workspace.recovery:${userKey()}:`; }

function legacyWorkspace(): WorkspaceDocument {
  try {
    const tabs = JSON.parse(localStorage.getItem(`rowset.editor.tabs:${userKey()}`) ?? "null");
    const active = localStorage.getItem(`rowset.editor.activeTab:${userKey()}`);
    const document = { version: 1, tabs, activeTabId: tabs?.some((tab: { id: string }) => tab?.id === active) ? active : tabs?.[0]?.id };
    if (validWorkspace(document)) return document;
  } catch { /* Leave invalid legacy storage untouched. */ }
  return { version: 1, tabs: [{ id: "q-1", title: "Query 1", sql: "" }], activeTabId: "q-1" };
}

function localRecoveries() {
  const found: { key: string; document: WorkspaceDocument; date: string }[] = [];
  try {
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i);
      if (!key?.startsWith(recoveryPrefix())) continue;
      try {
        const item = JSON.parse(localStorage.getItem(key) ?? "null");
        if (validWorkspace(item?.document)) found.push({ key, document: item.document, date: String(item.date ?? "") });
      } catch { /* Preserve unrecognized recovery data. */ }
    }
  } catch { /* Browser storage can be disabled. */ }
  return found.sort((a, b) => b.date.localeCompare(a.date));
}

export function exportWorkspace(workspace: WorkspaceDocument) {
  const url = URL.createObjectURL(new Blob([JSON.stringify(workspace, null, 2)], { type: "application/json" }));
  const link = document.createElement("a"); link.href = url; link.download = "rowset-workspace.json";
  link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
}

function forget(key: string) {
  try { localStorage.removeItem(key); } catch { /* Browser storage can be disabled. */ }
}

export default function WorkspaceGate({ children }: { children: (snapshot: WorkspaceSnapshot, initial: WorkspaceDocument) => ReactNode }) {
  const [snapshot, setSnapshot] = useState<WorkspaceSnapshot | null>(null);
  const [initial, setInitial] = useState<WorkspaceDocument | null>(null);
  // The workspace being assembled while the user decides about recovery copies.
  const [draft, setDraft] = useState<WorkspaceDocument | null>(null);
  const [recoveries, setRecoveries] = useState(localRecoveries);
  const [error, setError] = useState("");
  // A workspace encrypted with a key this installation no longer has is not
  // a transient failure: retrying will not help, so the card offers a way
  // forward instead of only "Try again".
  const [keyMismatch, setKeyMismatch] = useState(false);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let alive = true;
    api<WorkspaceSnapshot>("/workspace").then(value => {
      if (!Number.isSafeInteger(value.revision) || value.revision < 0 || (value.document !== null && !validWorkspace(value.document))) throw new Error("Invalid saved workspace. Existing drafts were not changed.");
      if (!alive) return;
      const base = value.document ?? legacyWorkspace();
      // Copies whose tabs were saved after all need no decision.
      const pending = localRecoveries().filter(item => {
        if (missingTabs(base, item.document).length) return true;
        forget(item.key);
        return false;
      });
      setSnapshot(value);
      setRecoveries(pending);
      if (pending.length) setDraft(base); else setInitial(base);
    }).catch(err => {
      if (!alive) return;
      setKeyMismatch(err instanceof ApiError && err.body.code === "WORKSPACE_KEY_MISMATCH");
      setError(err instanceof Error ? err.message : "Workspace unavailable");
    });
    return () => { alive = false; };
  }, [attempt]);

  if (error) {
    return (
      <GateCard title="Your workspace could not be loaded">
        <p>{error}</p>
        <div className="mt-4 flex flex-wrap gap-2">
          <Button onClick={() => { setError(""); setKeyMismatch(false); setAttempt(n => n + 1); }}>Try again</Button>
          {keyMismatch && (
            <LinkButton
              onClick={() => {
                if (!confirm("Start with an empty workspace? Your saved drafts stay in the database, but the first draft saved from here replaces them.")) return;
                setSnapshot({ revision: 0, document: null });
                setInitial(legacyWorkspace());
                setError("");
              }}
            >
              Start a new workspace
            </LinkButton>
          )}
          {recoveries.map(item => <LinkButton key={item.key} onClick={() => exportWorkspace(item.document)}>Download unsaved tabs ({formatDate(item.date)})</LinkButton>)}
        </div>
      </GateCard>
    );
  }
  if (!snapshot) return <p className="p-6 text-[13px] text-slate-500">Loading your workspace…</p>;
  if (!initial && draft) {
    const restore = (item: typeof recoveries[number]) => {
      try {
        const next = mergeWorkspace(draft, item.document, () => crypto.randomUUID());
        forget(item.key);
        const left = recoveries.filter(other => other.key !== item.key);
        setRecoveries(left);
        if (left.length) setDraft(next); else setInitial(next);
      } catch (err) { setError(err instanceof Error ? err.message : "Recovery failed"); }
    };
    const discard = (item: typeof recoveries[number]) => {
      forget(item.key);
      const left = recoveries.filter(other => other.key !== item.key);
      setRecoveries(left);
      if (!left.length) setInitial(draft);
    };
    return (
      <GateCard title="Unsaved tabs found">
        <p>Some tabs were not saved to your workspace before Rowset Studio closed. This browser kept a copy.</p>
        <ul className="mt-4 divide-y divide-slate-100 rounded-md border border-slate-200 dark:divide-slate-800 dark:border-slate-800">
          {recoveries.map(item => {
            const tabs = missingTabs(draft, item.document);
            return (
              <li key={item.key} className="flex flex-wrap items-center gap-3 px-3 py-2.5">
                <div className="min-w-0 flex-1">
                  <div className="text-[13px] font-medium text-slate-800 dark:text-slate-100">{tabs.length} unsaved tab{tabs.length === 1 ? "" : "s"}</div>
                  <div className="truncate text-[12px] text-slate-500" title={tabs.map(tab => tab.title).join(", ")}>{formatDate(item.date)} · {tabs.map(tab => tab.title).join(", ")}</div>
                </div>
                <LinkButton onClick={() => exportWorkspace(item.document)}>Download</LinkButton>
                <LinkButton onClick={() => discard(item)}>Discard</LinkButton>
                <Button onClick={() => restore(item)}>Restore tabs</Button>
              </li>
            );
          })}
        </ul>
        <div className="mt-4 flex items-center justify-between gap-3">
          <p className="text-[12px] text-slate-500">Restored tabs are added next to your saved ones; nothing is replaced.</p>
          <LinkButton onClick={() => setInitial(draft)}>Decide later</LinkButton>
        </div>
      </GateCard>
    );
  }
  if (!initial) return null;
  return children(snapshot, initial);
}

function GateCard({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="grid min-h-[60vh] place-items-center p-4">
      <Panel className="w-full max-w-xl p-5 text-[13px] text-slate-600 dark:text-slate-300">
        <h1 className="mb-1.5 text-[15px] font-semibold text-slate-900 dark:text-slate-100">{title}</h1>
        {children}
      </Panel>
    </div>
  );
}

function LinkButton({ onClick, children }: { onClick: () => void; children: ReactNode }) {
  return <button type="button" onClick={onClick} className="h-8 rounded-md px-2.5 text-[12px] text-slate-600 hover:bg-slate-100 hover:text-slate-900 dark:text-slate-300 dark:hover:bg-slate-800 dark:hover:text-slate-100">{children}</button>;
}

function formatDate(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

export function useWorkspacePersistence(snapshot: WorkspaceSnapshot, initial: WorkspaceDocument, current: WorkspaceDocument) {
  const [, refresh] = useState(0);
  const [storageError, setStorageError] = useState(false);
  const writerRef = useRef<WorkspaceWriter | null>(null);
  const key = useRef(`${recoveryPrefix()}${crypto.randomUUID()}`);
  const alive = useRef(false);
  if (!writerRef.current) writerRef.current = new WorkspaceWriter(snapshot, initial,
    (revision, document) => api("/workspace", { method: "PUT", body: JSON.stringify({ revision, document }) }),
    () => { if (alive.current) refresh(n => n + 1); });
  const writer = writerRef.current;
  writer.update(current);
  const backup = () => {
    try {
      if (writer.dirty) localStorage.setItem(key.current, JSON.stringify({ date: new Date().toISOString(), document: writer.current }));
      else localStorage.removeItem(key.current);
      if (alive.current) setStorageError(false);
    } catch { if (alive.current) setStorageError(true); }
  };
  const backupRef = useRef(backup); backupRef.current = backup;
  useEffect(() => {
    alive.current = true;
    const leave = (event: BeforeUnloadEvent) => { backupRef.current(); if (writer.dirty) { event.preventDefault(); event.returnValue = ""; } };
    const hide = () => { backupRef.current(); void writer.flush(); };
    window.addEventListener("beforeunload", leave); window.addEventListener("pagehide", hide);
    return () => { alive.current = false; backupRef.current(); void writer.flush().then(() => backupRef.current()); window.removeEventListener("beforeunload", leave); window.removeEventListener("pagehide", hide); };
  }, [writer]);
  useEffect(() => {
    // Saving encrypts every tab, so large workspaces wait longer between
    // saves instead of encrypting megabytes on each keystroke.
    const size = current.tabs.reduce((total, tab) => total + tab.sql.length, 0);
    const delay = size > 4_000_000 ? 5000 : size > 512_000 ? 2000 : 400;
    const backupTimer = setTimeout(() => backupRef.current(), Math.min(delay, 100));
    const timer = setTimeout(() => { void writer.flush().then(() => backupRef.current()); }, delay);
    return () => { clearTimeout(timer); clearTimeout(backupTimer); };
  }, [current.tabs, current.activeTabId, writer]);
  return { dirty: writer.dirty, error: writer.error?.message, storageError, retry: () => { void writer.retry().then(() => backupRef.current()); } };
}
