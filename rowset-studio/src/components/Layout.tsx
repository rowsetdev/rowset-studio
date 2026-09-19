import { Suspense, useEffect, useRef, useState } from "react";
import { Navigate, Outlet, useLocation, useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { Icon } from "./Icon";
import RowsetLogo from "./RowsetLogo";
import QuitButton from "./QuitButton";
import { useAuth } from "../lib/auth";
import { InstanceBoundary, useInstance, useShared } from "../lib/instance";
import { extensions, type NavGroup } from "../app/extensions";
import CommandPalette from "./CommandPalette";

// ProtectedLayout guards the app shell: unauthenticated users are redirected to
// login; authenticated users get the sidebar + topbar + routed content.
export default function ProtectedLayout() {
  const token = useAuth((s) => s.token);
  const status = useAuth((s) => s.status);
  const notices = extensions.flatMap((item) => item.notices ?? []);
  const location = useLocation();
  // The SQL editor always opens with the navigation collapsed, because the
  // explorer needs the width; expanding it there lasts for that visit only.
  // Every other page keeps the width last chosen on such a page.
  const editing = location.pathname.startsWith("/editor");
  const [sidebarSaved, setSidebarSaved] = useState(() => localStorage.getItem("rowset.sidebar") === "collapsed");
  const [sidebarOverride, setSidebarOverride] = useState<boolean | null>(null);
  useEffect(() => setSidebarOverride(null), [editing]);
  const sidebarCollapsed = sidebarOverride ?? (editing || sidebarSaved);

  useEffect(() => {
    localStorage.setItem("rowset.sidebar", sidebarSaved ? "collapsed" : "expanded");
  }, [sidebarSaved]);

  // Session restore (refresh-cookie exchange) is still in flight; don't bounce
  // to /login before it settles.
  if (!token && status === "restoring") {
    return (
      <div className="flex h-screen items-center justify-center bg-paper text-sm text-slate-400 dark:bg-[#121317]">
        Restoring session…
      </div>
    );
  }
  if (!token) return <Navigate to="/login" replace />;
  return (
    <div className="flex h-screen bg-paper text-ink dark:bg-[#121317] dark:text-slate-100">
      <CommandPalette />
      <Sidebar
        collapsed={sidebarCollapsed}
        onToggle={() => {
          setSidebarOverride(!sidebarCollapsed);
          if (!editing) setSidebarSaved(!sidebarCollapsed);
        }}
      />
      <main className="flex-1 overflow-auto p-3">
        {notices.map((Notice, index) => <Notice key={index} />)}
        <Suspense fallback={<div className="p-5 text-sm text-slate-400">Loading…</div>}>
		  <InstanceBoundary><Outlet /></InstanceBoundary>
        </Suspense>
      </main>
    </div>
  );
}

const personalGroups: NavGroup[] = [
  { title: "Personal workspace", items: [
    { to: "/editor", label: "Query", icon: "sql" },
    { to: "/notebooks", label: "Notebooks", icon: "notebook" },
    { to: "/activity", label: "Activity", icon: "activity" },
    { to: "/schedules", label: "Schedules", icon: "clock" },
  ] },
  { title: "Database", items: [
    { to: "/connections", label: "Connections", icon: "plug" },
    { to: "/schema-compare", label: "Schema comparison", icon: "table" },
  ] },
  { title: "Settings", items: [
    { to: "/policies", label: "My policies", icon: "shield" },
    { to: "/account", label: "Account", icon: "key" },
  ] },
];

// On a shared server the same pages, named for several users.
const sharedGroups: NavGroup[] = [
  { ...personalGroups[0], title: "Workspace" },
  personalGroups[1],
  { title: "Settings", items: [
    { to: "/policies", label: "Policies", icon: "shield", adminOnly: true },
    { to: "/account", label: "Account", icon: "key" },
  ] },
];

const appVersion = import.meta.env.VITE_ROWSET_VERSION || "dev";
const vendor = import.meta.env.VITE_ROWSET_VENDOR ?? "";
const commandPaletteShortcut = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform) ? "⇧⌘K" : "Ctrl+Shift+K";

function Sidebar({ collapsed, onToggle }: { collapsed: boolean; onToggle: () => void }) {
  const extensionGroups = extensions.flatMap((item) => item.navGroups ?? []);
  const sidebarWidgets = extensions.flatMap((item) => item.sidebarWidgets ?? []);
  const productLabel = extensions.find((item) => item.productLabel)?.productLabel ?? "Community";
  const shared = useShared();
  // The desktop app signs in by itself, so it has nothing to sign out of.
  const desktop = Boolean(useInstance().data?.desktop);
  // Extensions add groups before Settings; everything Studio has stays.
  const base = shared ? sharedGroups : personalGroups;
  const groups: NavGroup[] = [...base.slice(0, -1), ...extensionGroups, base[base.length - 1]];
  const user = useAuth((s) => s.user);
  const logout = useAuth((s) => s.logout);
  const navigate = useNavigate();
  const location = useLocation();
  const locationRef = useRef(location);
  locationRef.current = location;
  const qc = useQueryClient();
  // Drop all cached server state on sign-out so the next user never sees the
  // previous session's data.
  const signOut = () => {
    logout();
    qc.clear();
    navigate("/login");
  };
  const isAdmin = user?.role === "admin";
  const [theme, setTheme] = useState(() => localStorage.getItem("rowset.theme") || "light");
  const go = (to: string) => {
    void navigate(to);
    window.setTimeout(() => {
      const targetPath = new URL(to, window.location.href).pathname;
      // A streamed query can leave browser history on the destination while
      // React Router still renders the editor. Reload only that inconsistent
      // state. A blocked navigation does not change window.location, so its
      // confirmation flow remains untouched.
      if (locationRef.current.pathname !== targetPath && window.location.pathname === targetPath) window.location.reload();
    }, 150);
  };

  useEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark");
    localStorage.setItem("rowset.theme", theme);
    window.dispatchEvent(new CustomEvent("rowset:theme", { detail: theme }));
  }, [theme]);

  return (
    <aside className={`flex h-full shrink-0 flex-col border-r border-slate-200 bg-white transition-[width] duration-150 dark:border-slate-800 dark:bg-slate-950 ${collapsed ? "w-14" : "w-[208px]"}`}>
      <div className={`flex h-12 items-center border-b border-slate-200 dark:border-slate-800 ${collapsed ? "justify-center px-0" : "justify-between px-3"}`}>
        {!collapsed && (
          <div className="flex min-w-0 items-center gap-2">
            <RowsetLogo size={28} className="shrink-0" />
            <div className="min-w-0">
              <div className="text-[13px] font-semibold leading-tight tracking-wide text-slate-900 dark:text-slate-100">Rowset Studio</div>
              <div className="text-[10px] tracking-wide text-slate-500">{shared ? productLabel : "Community"}{vendor ? ` · by ${vendor}` : ""}</div>
            </div>
          </div>
        )}
        <button
          onClick={onToggle}
          className="grid h-7 w-7 place-items-center rounded-md border border-slate-200 bg-slate-50 text-slate-500 transition hover:bg-slate-100 hover:text-slate-800 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-400 dark:hover:text-slate-100"
          title={collapsed ? "Expand navigation" : "Collapse navigation"}
        >
          <Icon name={collapsed ? "chevron-right" : "chevron-left"} size={14} />
        </button>
      </div>
      <nav className="flex-1 space-y-3 overflow-y-auto p-2">
        {groups.map((group) => {
          if (group.adminOnly && !isAdmin) return null;
          return (
            <div key={group.title} className="space-y-1">
              {!collapsed && (
                <div className="flex items-center justify-between gap-2 px-2 text-[11px] font-semibold tracking-wide text-slate-500">
                  <span>{group.title}</span>
                  {group.title === "Personal workspace" && <kbd className="font-sans text-[10px] font-normal tracking-normal text-slate-400">{commandPaletteShortcut}</kbd>}
                </div>
              )}
              <div className="space-y-0.5">
                {group.items.filter((n) => !n.adminOnly || isAdmin).map((n) => {
                  // A plain button navigating with useNavigate, not a real
                  // <a href>: this is in-app routing only (there's no
                  // separate session to open in a new tab against a
                  // one-use local ticket), and a real href makes every
                  // browser show its target-URL preview on hover.
                  const isActive = n.end ? location.pathname === n.to : location.pathname === n.to || location.pathname.startsWith(n.to + "/");
                  return (
                    <button
                      key={n.to}
                      type="button"
                      onClick={() => go(n.to)}
                      title={n.label}
                      className={`group flex h-8 w-full items-center ${collapsed ? "justify-center px-0" : "justify-between px-2"} rounded-md border border-transparent text-[13px] transition ${
                        isActive
                          ? "border-brand-100 bg-brand-50 font-medium text-brand-900 dark:border-brand-400/20 dark:bg-brand-500/10 dark:text-slate-50"
                          : "text-slate-600 hover:bg-slate-50 dark:text-slate-300 dark:hover:bg-slate-900"
                      }`}
                    >
                      <span className="flex min-w-0 items-center gap-2.5">
                        <Icon
                          name={n.icon}
                          size={16}
                          className={isActive ? "text-brand-600 dark:text-brand-300" : "text-slate-400 group-hover:text-slate-600 dark:group-hover:text-slate-300"}
                        />
                        {!collapsed && <span className="truncate">{n.label}</span>}
                      </span>
                    </button>
                  );
                })}
              </div>
            </div>
          );
        })}
      </nav>

      <div className="shrink-0 border-t border-slate-200 p-2 dark:border-slate-800">
        {collapsed ? (
          <div className="flex flex-col items-center gap-1">
            {sidebarWidgets.map((Widget, index) => <Widget key={index} collapsed />)}
            <button
              onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
              className="grid h-8 w-8 place-items-center rounded-md text-slate-500 transition hover:bg-slate-100 hover:text-slate-800 dark:text-slate-400 dark:hover:bg-slate-900 dark:hover:text-slate-100"
              title={theme === "dark" ? "Switch to light" : "Switch to dark"}
            >
              <Icon name={theme === "dark" ? "sun" : "moon"} size={16} />
            </button>
            {!desktop && <button
              onClick={signOut}
              className="grid h-8 w-8 place-items-center rounded-md text-slate-500 transition hover:bg-slate-100 hover:text-slate-900 dark:text-slate-400 dark:hover:bg-slate-900 dark:hover:text-slate-100"
              title="Sign out"
            >
              <Icon name="logout" size={16} />
            </button>}
            <QuitButton collapsed />
          </div>
        ) : (
          <div className="space-y-1.5">
            {desktop ? (
              <div className="flex items-center justify-center gap-3">
                <span className="whitespace-nowrap text-[10px] leading-none text-slate-400" title={`Rowset ${appVersion}`}>
                  Rowset v{appVersion}
                </span>
                <span aria-hidden className="h-4 w-px bg-slate-200 dark:bg-slate-800" />
                <div className="flex items-center justify-center gap-0.5">
                  <QuitButton collapsed={false} />
                  {sidebarWidgets.map((Widget, index) => <Widget key={index} collapsed={false} />)}
                </div>
                <span aria-hidden className="h-4 w-px bg-slate-200 dark:bg-slate-800" />
                <button
                  onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
                  className="grid h-7 w-7 shrink-0 place-items-center justify-self-end rounded-md text-slate-500 transition hover:bg-slate-100 hover:text-slate-800 dark:text-slate-400 dark:hover:bg-slate-900 dark:hover:text-slate-100"
                  title={theme === "dark" ? "Switch to light" : "Switch to dark"}
                >
                  <Icon name={theme === "dark" ? "sun" : "moon"} size={15} />
                </button>
              </div>
            ) : (<>
            <div className="flex items-center justify-between rounded-md px-2 py-1.5">
              {desktop ? <span /> : (
              <div className="flex min-w-0 items-center gap-2">
                <span className="grid h-7 w-7 shrink-0 place-items-center rounded-full bg-slate-100 text-[11px] font-semibold uppercase text-slate-600 dark:bg-slate-800 dark:text-slate-300">
                  {(user?.email ?? "?").slice(0, 1)}
                </span>
                <div className="min-w-0">
                  <div className="truncate text-[13px] font-medium text-slate-800 dark:text-slate-200">{user?.email}</div>
                  {user?.role && <div className="text-[11px] text-slate-400">Role: {titleCase(roleLabel(user.role))}</div>}
                </div>
              </div>
              )}
              <button
                onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
                className="grid h-7 w-7 shrink-0 place-items-center rounded-md text-slate-500 transition hover:bg-slate-100 hover:text-slate-800 dark:text-slate-400 dark:hover:bg-slate-900 dark:hover:text-slate-100"
                title={theme === "dark" ? "Switch to light" : "Switch to dark"}
              >
                <Icon name={theme === "dark" ? "sun" : "moon"} size={15} />
              </button>
              {sidebarWidgets.map((Widget, index) => <Widget key={index} collapsed={false} />)}
            </div>
            {!desktop && <button
              onClick={signOut}
              className="flex h-8 w-full items-center gap-2 rounded-md px-2 text-[13px] text-slate-600 transition hover:bg-slate-50 hover:text-slate-900 dark:text-slate-300 dark:hover:bg-slate-900 dark:hover:text-slate-100"
            >
              <Icon name="logout" size={15} className="text-slate-400" />
              Sign out
            </button>}
            </>)}
          </div>
        )}
        {(!desktop || collapsed) && (
          <div className={`mt-1.5 border-t border-slate-100 pt-1.5 text-[10px] text-slate-400 dark:border-slate-800 ${collapsed ? "text-center" : "px-2"}`} title={`Rowset ${appVersion}`}>
            {collapsed ? `v${appVersion}` : `Rowset v${appVersion}`}
          </div>
        )}
      </div>
    </aside>
  );
}

function roleLabel(role: string) {
  if (role === "viewer") return "developer";
  if (role === "editor") return "support";
  return role;
}

function titleCase(s: string) {
  return s.charAt(0).toUpperCase() + s.slice(1);
}
