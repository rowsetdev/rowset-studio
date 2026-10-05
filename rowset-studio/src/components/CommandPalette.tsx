import { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useNavigate } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { Icon, type IconName } from "./Icon";
import EngineLogo from "./EngineLogo";
import { listConnections } from "../features/connections/api";

interface PaletteItem {
  id: string;
  label: string;
  hint?: string;
  group: string;
  icon?: IconName;
  engine?: string;
  run: () => void;
}

const PAGES: { to: string; label: string; icon: IconName; hint: string }[] = [
  { to: "/editor", label: "Query", icon: "sql", hint: "SQL editor" },
  { to: "/console", label: "Console", icon: "terminal", hint: "psql, mysql, mongosh and the rest, in the browser" },
  { to: "/notebooks", label: "Notebooks", icon: "notebook", hint: "Markdown + SQL notes" },
  { to: "/schedules", label: "Schedules", icon: "clock", hint: "Scheduled queries" },
  { to: "/schema-compare", label: "Schema comparison", icon: "table", hint: "Compare two schemas" },
  { to: "/connections", label: "Connections", icon: "plug", hint: "Manage saved connections" },
  { to: "/activity", label: "Activity", icon: "activity", hint: "Your statements across connections" },
  { to: "/policies", label: "My policies", icon: "shield", hint: "Guardrail policies" },
  { to: "/account", label: "Account", icon: "key", hint: "Sign-in and password" },
];

// Cmd+K / Ctrl+K anywhere in the app opens a quick-jump palette to pages and
// saved connections, the way every competing desktop DB client has one.
export default function CommandPalette() {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [highlight, setHighlight] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const navigate = useNavigate();
  const { data: connections = [] } = useQuery({ queryKey: ["connections"], queryFn: listConnections, enabled: open });

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      // Shift+Cmd/Ctrl+K: the schema explorer already owns plain Cmd/Ctrl+K
      // (it focuses the table search box), and Monaco binds the same
      // combination to "Delete Line" while the editor has focus. Both are
      // capturing listeners closer to the target, so this one must also
      // capture (and stop the event) to win regardless of where focus is.
      if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key.toLowerCase() === "k") {
        e.preventDefault();
        e.stopPropagation();
        setOpen((v) => !v);
      } else if (e.key === "Escape" && open) {
        setOpen(false);
      }
    }
    window.addEventListener("keydown", onKeyDown, true);
    return () => window.removeEventListener("keydown", onKeyDown, true);
  }, [open]);

  useEffect(() => {
    if (open) {
      setQuery("");
      setHighlight(0);
      // Focus after the portal mounts.
      requestAnimationFrame(() => inputRef.current?.focus());
    }
  }, [open]);

  const items = useMemo<PaletteItem[]>(() => {
    const pages: PaletteItem[] = PAGES.map((page) => ({
      id: `page:${page.to}`,
      label: page.label,
      hint: page.hint,
      group: "Go to",
      icon: page.icon,
      run: () => navigate(page.to),
    }));
    const nameCounts = new Map<string, number>();
    for (const c of connections) {
      const key = c.name.trim().toLowerCase();
      nameCounts.set(key, (nameCounts.get(key) ?? 0) + 1);
    }
    const conns: PaletteItem[] = connections.map((c) => {
      const dup = (nameCounts.get(c.name.trim().toLowerCase()) ?? 0) > 1;
      const address = c.engine === "sqlite" || c.engine === "duckdb" ? c.database : `${c.host}:${c.port}/${c.database}`;
      return {
        id: `connection:${c.id}`,
        label: dup ? `${c.name} (${address})` : c.name,
        hint: address,
        group: "Connections",
        engine: c.engine,
        run: () => navigate("/editor", { state: { openConnection: true, connectionId: c.id } }),
      };
    });
    return [...pages, ...conns];
  }, [connections, navigate]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return items;
    return items
      .map((item) => ({ item, score: matchScore(item.label, q) }))
      .filter((x) => x.score > 0)
      .sort((a, b) => b.score - a.score)
      .map((x) => x.item);
  }, [items, query]);

  useEffect(() => setHighlight(0), [query]);

  if (!open) return null;

  function activate(item: PaletteItem) {
    item.run();
    setOpen(false);
  }

  let lastGroup = "";

  return createPortal(
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto bg-slate-950/40 p-4 pt-[12vh]" onClick={() => setOpen(false)}>
      <div className="w-full max-w-lg overflow-hidden rounded-lg border border-slate-200 bg-white shadow-2xl dark:border-slate-800 dark:bg-slate-950" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center gap-2 border-b border-slate-200 px-3 dark:border-slate-800">
          <Icon name="search" size={15} className="shrink-0 text-slate-400" />
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") { e.preventDefault(); setHighlight((h) => Math.min(h + 1, filtered.length - 1)); }
              else if (e.key === "ArrowUp") { e.preventDefault(); setHighlight((h) => Math.max(h - 1, 0)); }
              else if (e.key === "Enter") { e.preventDefault(); if (filtered[highlight]) activate(filtered[highlight]); }
            }}
            placeholder="Jump to a page or connection…"
            className="h-11 flex-1 bg-transparent text-[13px] outline-none placeholder:text-slate-400 dark:text-slate-100"
          />
          <kbd className="rounded border border-slate-200 px-1.5 py-0.5 text-[10px] text-slate-400 dark:border-slate-700">esc</kbd>
        </div>
        <div className="max-h-80 overflow-y-auto p-1.5">
          {filtered.length === 0 && <p className="px-3 py-6 text-center text-[12px] text-slate-400">No matches.</p>}
          {filtered.map((item, index) => {
            const showGroup = item.group !== lastGroup;
            lastGroup = item.group;
            return (
              <div key={item.id}>
                {showGroup && <div className="px-2 pb-1 pt-2 text-[10px] font-semibold uppercase tracking-wide text-slate-400">{item.group}</div>}
                <button
                  type="button"
                  onMouseEnter={() => setHighlight(index)}
                  onClick={() => activate(item)}
                  className={`flex w-full items-center gap-2.5 rounded-md px-2.5 py-1.5 text-left text-[13px] ${
                    index === highlight ? "bg-brand-50 text-brand-900 dark:bg-brand-500/10 dark:text-slate-50" : "text-slate-700 dark:text-slate-200"
                  }`}
                >
                  {item.engine ? <EngineLogo engine={item.engine} size={15} /> : item.icon && <Icon name={item.icon} size={15} className="shrink-0 text-slate-400" />}
                  <span className="min-w-0 flex-1 truncate">{item.label}</span>
                  {item.hint && <span className="shrink-0 truncate text-[11px] text-slate-400">{item.hint}</span>}
                </button>
              </div>
            );
          })}
        </div>
      </div>
    </div>,
    document.body,
  );
}

// Each space-separated word of the query must appear somewhere in the label
// (in any order), so "postgres demo" finds "PostgreSQL Demo". Prefix and
// whole-string matches rank highest so exact typing still sorts first.
function matchScore(label: string, query: string): number {
  const value = label.toLowerCase();
  if (value === query) return 100;
  if (value.startsWith(query)) return 80;
  const words = query.split(/\s+/).filter(Boolean);
  if (words.length === 0) return 0;
  if (!words.every((word) => value.includes(word))) return 0;
  return words.length > 1 ? 55 : value.includes(" " + query) || value.includes("-" + query) ? 60 : 40;
}
