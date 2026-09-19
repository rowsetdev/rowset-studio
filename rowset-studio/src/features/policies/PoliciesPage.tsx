import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { Icon, type IconName } from "../../components/Icon";
import { Button, Input, Modal, PageHeader, Panel, SegTabs, Select } from "../../components/ui";
import { EnvBadge, envKind } from "../../components/EnvBadge";
import { api } from "../../lib/api";
import { useShared } from "../../lib/instance";
import { useActiveExtensions, type PolicyAction, type PolicyTemplate } from "../../app/extensions";
import { listConnections } from "../connections/api";

interface PolicyOverride {
  scope: "connection" | "role";
  target: string;
  enabled: boolean;
  value?: string | null;
}

interface Policy {
  key: string;
  description: string;
  detail?: string;
  risk: string;
  enabled: boolean;
  scope?: "global" | "connection" | "role";
  globalEnabled?: boolean;
  overrides?: PolicyOverride[];
  hasValue?: boolean;
  value?: string | null;
}

const ENV_ORDER: Record<string, number> = { dev: 0, test: 1, prod: 2 };

function sortByEnv<T extends { name: string; environment: string }>(items: T[]): T[] {
  return [...items].sort((a, b) => {
    const byEnv = ENV_ORDER[envKind(a.environment)] - ENV_ORDER[envKind(b.environment)];
    return byEnv !== 0 ? byEnv : a.name.localeCompare(b.name);
  });
}

function listPolicies(connectionId: string, role: string) {
  const params = new URLSearchParams();
  if (connectionId !== "*") params.set("connectionId", connectionId);
  if (role !== "*") params.set("role", role);
  const qs = params.toString();
  return api<{ policies: Policy[] }>(`/policies${qs ? `?${qs}` : ""}`).then((r) => r.policies);
}

type ActionKind = string;

interface CustomPolicy {
  id: string;
  name: string;
  kind: string;
  config: string;
  connectionId: string;
  role: string;
  enabled: boolean;
  createdAt: string;
}

// Enforcement templates a custom policy can be built from.
const POLICY_TEMPLATES: PolicyTemplate[] = [
  { kind: "deny_table", label: "Block a table", configLabel: "Table name", hint: "Any statement touching this table is denied." },
  { kind: "deny_schema", label: "Block a schema", configLabel: "Schema name", hint: "Any statement on a table in this schema is denied." },
  { kind: "deny_statement", label: "Block a statement type", configLabel: "Statement", hint: "insert, update, delete or ddl.", statement: true },
  { kind: "limit_rows", label: "Limit result rows", configLabel: "Row limit", hint: "SELECT results are limited in the database before streaming." },
  { kind: "deny_write_outside_hours", label: "Writes only during work hours", configLabel: "Time window", hint: "Writes outside this window (server time) are denied, e.g. 09:00-18:00." },
];

const STATEMENT_KINDS = ["insert", "update", "delete", "ddl"];

function listCustomPolicies() {
  return api<{ policies: CustomPolicy[] }>("/policies/custom").then((r) => r.policies);
}

export default function PoliciesPage() {
  const community = !useShared();
  const extraActions = useActiveExtensions().flatMap((item) => item.policyActions ?? []);
  const qc = useQueryClient();
  const [connectionId, setConnectionId] = useState("*");
  const [role, setRole] = useState("*");
  const { data: rawConnections = [] } = useQuery({ queryKey: ["connections"], queryFn: listConnections });
  // Group by environment (dev, then test/staging, then prod) so scope
  // pickers don't force you to infer environment from the name.
  const connections = useMemo(() => sortByEnv(rawConnections), [rawConnections]);
  const policyRoles = useActiveExtensions().find((item) => item.policyRoles)?.policyRoles;
  const { data: allRoles = [] } = useQuery({ queryKey: ["roles"], queryFn: () => policyRoles!(), enabled: Boolean(policyRoles) });
  // Admin is god-mode and bypasses policy scoping entirely -- never offer it here.
  const roles = useMemo(() => allRoles.filter((r) => r.name !== "admin"), [allRoles]);
  const KEY = ["policies", connectionId, role];
  const { data, isLoading, isError } = useQuery({ queryKey: KEY, queryFn: () => listPolicies(connectionId, role) });
  const [action, setAction] = useState<ActionKind>("all");
  const [enabledOnly, setEnabledOnly] = useState(false);
  const [search, setSearch] = useState("");

  const connName = useMemo(
    () => new Map(connections.map((c) => [c.id, c.name])),
    [connections],
  );

  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<CustomPolicy | null>(null);
  const [scopeEditing, setScopeEditing] = useState<Policy | null>(null);
  const { data: customs = [] } = useQuery({ queryKey: ["custom-policies"], queryFn: listCustomPolicies });

  const patchCustom = useMutation({
    mutationFn: (p: CustomPolicy) =>
      api(`/policies/custom/${p.id}`, { method: "PATCH", body: JSON.stringify({ enabled: !p.enabled }) }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["custom-policies"] }),
  });
  const deleteCustom = useMutation({
    mutationFn: (id: string) => api(`/policies/custom/${id}`, { method: "DELETE" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["custom-policies"] }),
  });

  const toggle = useMutation({
    mutationFn: ({ p, enabled, value }: { p: Policy; enabled: boolean; value?: string }) =>
      api(`/policies/${p.key}`, {
        method: "PATCH",
        body: JSON.stringify({
          enabled,
          value,
          connectionId: connectionId === "*" ? undefined : connectionId,
          role: role === "*" ? undefined : role,
        }),
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["policies"] }),
  });

  const all = data ?? [];
  const rows = all.filter((p) => {
    if (action !== "all" && inferAction(p.key, extraActions) !== action) return false;
    if (enabledOnly && !p.enabled) return false;
    if (search && !`${p.key} ${p.description}`.toLowerCase().includes(search.toLowerCase())) return false;
    return true;
  });

  const count = (a: ActionKind) =>
    all.filter((p) => (a === "all" ? true : inferAction(p.key, extraActions) === a) && (!enabledOnly || p.enabled)).length;

  const tabs: { value: ActionKind; label: string; icon: IconName; count: number }[] = [
    { value: "all", label: "All", icon: "shield", count: count("all") },
    { value: "block", label: "Block", icon: "alert", count: count("block") },
    ...extraActions.map((item) => ({ value: item.value, label: item.label, icon: item.icon, count: count(item.value) })),
  ];

  // Scope shown next to the toggle: what a click would change.
  const toggleScope =
    role !== "*" ? `role: ${role}` : connectionId !== "*" ? (connName.get(connectionId) ?? "connection") : "all connections";

  return (
    <div className="space-y-5">
      <PageHeader
        icon="shield"
        title="Policies"
        subtitle={community ? "Your default and custom policies, enforced before queries reach a database." : "SQL guardrails, enforced before queries reach a database."}
      />

      <div className="flex flex-wrap items-end justify-between gap-3">
        <SegTabs tabs={tabs} value={action} onChange={setAction} />
        <div className="flex items-center gap-2 pb-1">
          <div className="w-44">
            <Select value={connectionId} onChange={(e) => setConnectionId(e.target.value)}>
              <option value="*">All connections</option>
              {connections.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </Select>
          </div>
          <div className="w-36" hidden={community}>
            <Select value={role} onChange={(e) => setRole(e.target.value)}>
              <option value="*">All roles</option>
              {roles.map((r) => (
                <option key={r.id} value={r.name}>
                  {r.name}
                </option>
              ))}
            </Select>
          </div>
          <button
            onClick={() => setEnabledOnly((v) => !v)}
            className={`inline-flex h-8 items-center gap-1.5 rounded-md border px-2.5 text-[12px] font-medium transition ${
              enabledOnly
                ? "border-slate-300 bg-white text-slate-700 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200"
                : "border-transparent text-slate-500 hover:text-slate-800 dark:text-slate-400"
            }`}
          >
            <Icon name="check" size={14} />
            Enabled only
          </button>
          <div className="relative w-56">
            <Icon name="search" size={14} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-slate-400" />
            <Input className="pl-8" placeholder="Search policies…" value={search} onChange={(e) => setSearch(e.target.value)} />
          </div>
          <Button onClick={() => setAdding(true)} className="inline-flex items-center gap-1.5">
            <Icon name="plus" size={14} /> Add policy
          </Button>
        </div>
      </div>

      {isLoading && <p className="text-[13px] text-slate-500">Loading…</p>}
      {isError && <p className="text-[13px] text-rose-600 dark:text-rose-400">Admin access required.</p>}
      {data && rows.length === 0 && !isLoading && (
        <Panel className="p-10 text-center">
          <p className="text-[13px] font-medium text-slate-700 dark:text-slate-200">No policies match</p>
          <p className="mt-1 text-[12px] text-slate-500">Try another action filter or clear the search.</p>
        </Panel>
      )}

      {rows.length > 0 && (
        <div className="space-y-2">
          <div className="flex items-center gap-3 px-3.5 text-[11px] font-semibold text-slate-400">
            <span className="w-9 shrink-0" />
            <span className="min-w-0 flex-1">Policy</span>
            <span className="hidden w-44 shrink-0 md:block">Connections</span>
            <span className="hidden w-36 shrink-0 md:block">Roles</span>
            <span className="hidden w-20 shrink-0 sm:block">Action</span>
            <span className="w-9 shrink-0 text-right" />
          </div>
          {rows.map((p) => (
            <PolicyRow
              key={p.key}
              policy={p}
              connName={connName}
              toggleScope={toggleScope}
              busy={toggle.isPending}
              onToggle={(enabled, value) => toggle.mutate({ p, enabled, value })}
              onEditScope={() => setScopeEditing(p)}
            />
          ))}
        </div>
      )}
      {customs.length > 0 && (
        <div className="space-y-2">
          <h2 className="px-1 text-[12px] font-semibold uppercase tracking-wide text-slate-400">Custom policies</h2>
          {customs.map((p) => (
            <Panel key={p.id} className="flex items-center gap-3 px-3.5 py-3">
              <span className="grid h-9 w-9 shrink-0 place-items-center rounded-md border border-violet-200 bg-violet-50 text-violet-600 dark:border-violet-400/20 dark:bg-violet-500/10 dark:text-violet-300">
                <Icon name="shield" size={16} />
              </span>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="truncate text-[13px] font-medium text-slate-900 dark:text-slate-100">{p.name}</span>
                  <span className="inline-flex h-[18px] shrink-0 items-center rounded border border-slate-200 bg-slate-50 px-1.5 font-mono text-[10px] text-slate-500 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-400">
                    {p.kind}: {p.config}
                  </span>
                </div>
                <div className="mt-0.5 truncate text-[11px] text-slate-400">
                  {p.connectionId ? (connName.get(p.connectionId) ?? p.connectionId) : "All connections"} · {p.role || "All roles"}
                </div>
              </div>
              <button
                onClick={() => setEditing(p)}
                title="Edit policy"
                className="text-slate-400 transition hover:text-slate-700 dark:hover:text-slate-200"
              >
                <Icon name="pencil" size={14} />
              </button>
              <button
                onClick={() => deleteCustom.mutate(p.id)}
                disabled={deleteCustom.isPending}
                title="Delete policy"
                className="text-slate-400 transition hover:text-rose-600 dark:hover:text-rose-400"
              >
                <Icon name="close" size={15} />
              </button>
              <Toggle on={p.enabled} disabled={patchCustom.isPending} onClick={() => patchCustom.mutate(p)} />
            </Panel>
          ))}
        </div>
      )}

      {(adding || editing) && (
        <AddPolicyModal
          connections={connections}
          roles={roles}
          existing={editing}
          onClose={() => { setAdding(false); setEditing(null); }}
          onCreated={() => {
            setAdding(false);
            setEditing(null);
            qc.invalidateQueries({ queryKey: ["custom-policies"] });
          }}
        />
      )}
      {scopeEditing && (
        <ScopeModal
          policy={scopeEditing}
          connections={connections}
          roles={roles}
          onClose={() => setScopeEditing(null)}
          onSaved={() => {
            setScopeEditing(null);
            qc.invalidateQueries({ queryKey: ["policies"] });
          }}
        />
      )}
    </div>
  );
}

// Edits which connections and roles a built-in policy applies to. Connection
// scope and role scope are independent axes (matching how overrides are
// stored and displayed as separate chip rows) -- not intersecting combos.
function ScopeModal({
  policy,
  connections,
  roles,
  onClose,
  onSaved,
}: {
  policy: Policy;
  connections: { id: string; name: string; environment: string }[];
  roles: { id: string; name: string }[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const shared = useShared();
  const overrides = policy.overrides ?? [];
  const existingConn = overrides.filter((o) => o.scope === "connection").map((o) => o.target);
  const existingRole = overrides.filter((o) => o.scope === "role").map((o) => o.target);

  const [connMode, setConnMode] = useState<"all" | "specific">(existingConn.length > 0 ? "specific" : "all");
  const [connSelected, setConnSelected] = useState<string[]>(existingConn);
  const [roleMode, setRoleMode] = useState<"all" | "specific">(existingRole.length > 0 ? "specific" : "all");
  const [roleSelected, setRoleSelected] = useState<string[]>(existingRole);
  const [enabled, setEnabled] = useState(policy.globalEnabled ?? policy.enabled);
  const [value, setValue] = useState(policy.value ?? "");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);

  function toggleIn(list: string[], id: string) {
    return list.includes(id) ? list.filter((v) => v !== id) : [...list, id];
  }

  async function save() {
    if (policy.hasValue && enabled && !(Number(value) > 0)) {
      setError("This policy requires a positive numeric value");
      return;
    }
    setSaving(true);
    setError("");
    const val = policy.hasValue ? value.trim() || undefined : undefined;
    try {
      await api(`/policies/${policy.key}`, { method: "PATCH", body: JSON.stringify({ enabled, value: val }) });

      const removeConn = connMode === "all" ? existingConn : existingConn.filter((t) => !connSelected.includes(t));
      const removeRole = roleMode === "all" ? existingRole : existingRole.filter((t) => !roleSelected.includes(t));
      for (const id of removeConn) {
        await api(`/policies/${policy.key}?connectionId=${encodeURIComponent(id)}`, { method: "DELETE" });
      }
      for (const name of removeRole) {
        await api(`/policies/${policy.key}?role=${encodeURIComponent(name)}`, { method: "DELETE" });
      }
      if (connMode === "specific") {
        for (const id of connSelected) {
          await api(`/policies/${policy.key}`, {
            method: "PATCH",
            body: JSON.stringify({ enabled, value: val, connectionId: id }),
          });
        }
      }
      if (roleMode === "specific") {
        for (const name of roleSelected) {
          await api(`/policies/${policy.key}`, {
            method: "PATCH",
            body: JSON.stringify({ enabled, value: val, role: name }),
          });
        }
      }
      onSaved();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to save policy");
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal title="Edit policy" onClose={onClose}>
      <div className="space-y-4">
        <div>
          <p className="text-[13px] font-medium text-slate-800 dark:text-slate-200">{policy.description}</p>
          {policy.detail && (
            <p className="mt-1 text-[12px] leading-relaxed text-slate-500 dark:text-slate-400">{policy.detail}</p>
          )}
        </div>

        <div className="flex items-center justify-between rounded-md border border-slate-200 px-3 py-2 dark:border-slate-800">
          <span className="text-[13px] font-medium text-slate-700 dark:text-slate-300">Enabled</span>
          <Toggle on={enabled} onClick={() => setEnabled((v) => !v)} />
        </div>

        {policy.hasValue && (
          <div>
            <label className="mb-1 block text-[12px] font-medium text-slate-600 dark:text-slate-300">{policy.key === "query_timeout_seconds" ? "Timeout (seconds)" : "Row limit"}</label>
            <Input type="number" min={1} max={policy.key === "query_timeout_seconds" ? 86400 : undefined} value={value} onChange={(e) => setValue(e.target.value)} placeholder={policy.key === "query_timeout_seconds" ? "30" : "7"} />
          </div>
        )}

        <div>
          <label className="mb-1 block text-[12px] font-medium text-slate-600 dark:text-slate-300">Connections</label>
          <div className="mb-2 flex gap-1.5">
            <ModeButton active={connMode === "all"} onClick={() => setConnMode("all")}>All</ModeButton>
            <ModeButton active={connMode === "specific"} onClick={() => setConnMode("specific")}>Specific</ModeButton>
          </div>
          {connMode === "specific" && (
            <div className="max-h-[160px] overflow-auto rounded-md border border-slate-200 dark:border-slate-800">
              {connections.length === 0 && <p className="p-2 text-[12px] text-slate-400">No connections.</p>}
              {connections.map((c) => (
                <label key={c.id} className="flex items-center gap-2 border-b border-slate-100 px-2.5 py-1.5 text-[13px] last:border-b-0 dark:border-slate-800">
                  <input
                    type="checkbox"
                    checked={connSelected.includes(c.id)}
                    onChange={() => setConnSelected((prev) => toggleIn(prev, c.id))}
                  />
                  <span className="min-w-0 flex-1 truncate">{c.name}</span>
                  <EnvBadge env={c.environment} />
                </label>
              ))}
            </div>
          )}
        </div>

        <div hidden={!shared}>
          <label className="mb-1 block text-[12px] font-medium text-slate-600 dark:text-slate-300">Roles</label>
          <div className="mb-2 flex gap-1.5">
            <ModeButton active={roleMode === "all"} onClick={() => setRoleMode("all")}>All</ModeButton>
            <ModeButton active={roleMode === "specific"} onClick={() => setRoleMode("specific")}>Specific</ModeButton>
          </div>
          {roleMode === "specific" && (
            <div className="max-h-[160px] overflow-auto rounded-md border border-slate-200 dark:border-slate-800">
              {roles.length === 0 && <p className="p-2 text-[12px] text-slate-400">No roles.</p>}
              {roles.map((r) => (
                <label key={r.id} className="flex items-center gap-2 border-b border-slate-100 px-2.5 py-1.5 text-[13px] last:border-b-0 dark:border-slate-800">
                  <input
                    type="checkbox"
                    checked={roleSelected.includes(r.name)}
                    onChange={() => setRoleSelected((prev) => toggleIn(prev, r.name))}
                  />
                  {r.name}
                </label>
              ))}
            </div>
          )}
        </div>

        {error && <p className="text-[12px] text-rose-600 dark:text-rose-400">{error}</p>}
        <div className="flex justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            className="inline-flex h-8 items-center rounded-md border border-slate-200 bg-white px-3 text-[13px] font-medium text-slate-600 transition hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800"
          >
            Cancel
          </button>
          <Button onClick={save} disabled={saving}>{saving ? "Saving…" : "Save"}</Button>
        </div>
      </div>
    </Modal>
  );
}

function ModeButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: string }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`h-7 rounded-md border px-2.5 text-[12px] font-medium transition ${
        active
          ? "border-slate-700 bg-slate-700 text-white dark:border-slate-300 dark:bg-slate-300 dark:text-slate-900"
          : "border-slate-200 bg-white text-slate-600 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800"
      }`}
    >
      {children}
    </button>
  );
}

// Create/edit modal: pick a template, set its parameter and scope.
function AddPolicyModal({
  connections,
  roles,
  existing,
  onClose,
  onCreated,
}: {
  connections: { id: string; name: string; environment: string }[];
  roles: { id: string; name: string }[];
  existing?: CustomPolicy | null;
  onClose: () => void;
  onCreated: () => void;
}) {
  const shared = useShared();
  const templates = [...POLICY_TEMPLATES, ...useActiveExtensions().flatMap((item) => item.policyTemplates ?? [])];
  const [kind, setKind] = useState(existing?.kind ?? POLICY_TEMPLATES[0].kind);
  const [name, setName] = useState(existing?.name ?? "");
  const [config, setConfig] = useState(existing?.config ?? "");
  const [connMode, setConnMode] = useState<"all" | "specific">(existing?.connectionId ? "specific" : "all");
  const [connSelected, setConnSelected] = useState<string[]>(existing?.connectionId ? [existing.connectionId] : []);
  const [roleMode, setRoleMode] = useState<"all" | "specific">(existing?.role ? "specific" : "all");
  const [roleSelected, setRoleSelected] = useState<string[]>(existing?.role ? [existing.role] : []);
  const [error, setError] = useState("");
  const template = templates.find((t) => t.kind === kind) ?? templates[0];

  function toggleIn(list: string[], id: string) {
    return list.includes(id) ? list.filter((v) => v !== id) : [...list, id];
  }

  const create = useMutation({
    mutationFn: async () => {
      if (connMode === "specific" && connSelected.length === 0) {
        throw new Error("Select at least one connection, or choose All");
      }
      if (roleMode === "specific" && roleSelected.length === 0) {
        throw new Error("Select at least one role, or choose All");
      }
      const connIds: (string | undefined)[] = connMode === "all" ? [undefined] : connSelected;
      const roleNames: (string | undefined)[] = roleMode === "all" ? [undefined] : roleSelected;
      const label = name.trim() || `${template.label}: ${config.trim()}`;
      const combos: { connectionId?: string; role?: string }[] = [];
      for (const connectionId of connIds) {
        for (const role of roleNames) {
          combos.push({ connectionId, role });
        }
      }
      if (existing) {
        await api(`/policies/custom/${existing.id}`, { method: "DELETE" });
      }
      for (const combo of combos) {
        await api("/policies/custom", {
          method: "POST",
          body: JSON.stringify({ name: label, kind, config: config.trim(), ...combo }),
        });
      }
    },
    onSuccess: onCreated,
    onError: (e) => setError(e instanceof Error ? e.message : "Failed to save policy"),
  });

  return (
    <Modal title={existing ? "Edit policy" : "Add policy"} onClose={onClose}>
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (config.trim()) create.mutate();
        }}
      >
        <div>
          <label className="mb-1 block text-[12px] font-medium text-slate-600 dark:text-slate-300">Template</label>
          <Select value={kind} onChange={(e) => { setKind(e.target.value); setConfig(""); }}>
            {templates.map((t) => (
              <option key={t.kind} value={t.kind}>{t.label}</option>
            ))}
          </Select>
          <p className="mt-1 text-[11px] text-slate-400">{template.hint}</p>
        </div>
        <div>
          <label className="mb-1 block text-[12px] font-medium text-slate-600 dark:text-slate-300">{template.configLabel}</label>
          {template.statement ? (
            <Select value={config} onChange={(e) => setConfig(e.target.value)}>
              <option value="">Choose…</option>
              {STATEMENT_KINDS.map((v) => (
                <option key={v} value={v}>{v.toUpperCase()}</option>
              ))}
            </Select>
          ) : (
            <Input
              value={config}
              onChange={(e) => setConfig(e.target.value)}
              type={kind === "limit_rows" ? "number" : "text"}
              min={kind === "limit_rows" ? 1 : undefined}
              placeholder={kind === "limit_rows" ? "7" : kind === "deny_write_outside_hours" ? "09:00-18:00" : "customers"}
            />
          )}
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className="mb-1 block text-[12px] font-medium text-slate-600 dark:text-slate-300">Connections</label>
            <div className="mb-1.5 flex gap-1.5">
              <ModeButton active={connMode === "all"} onClick={() => setConnMode("all")}>All</ModeButton>
              <ModeButton active={connMode === "specific"} onClick={() => setConnMode("specific")}>Specific</ModeButton>
            </div>
            {connMode === "specific" && (
              <div className="max-h-[140px] overflow-auto rounded-md border border-slate-200 dark:border-slate-800">
                {connections.length === 0 && <p className="p-2 text-[12px] text-slate-400">No connections.</p>}
                {connections.map((c) => (
                  <label key={c.id} className="flex items-center gap-2 border-b border-slate-100 px-2.5 py-1.5 text-[13px] last:border-b-0 dark:border-slate-800">
                    <input type="checkbox" checked={connSelected.includes(c.id)} onChange={() => setConnSelected((prev) => toggleIn(prev, c.id))} />
                    <span className="min-w-0 flex-1 truncate">{c.name}</span>
                    <EnvBadge env={c.environment} />
                  </label>
                ))}
              </div>
            )}
          </div>
          <div hidden={!shared}>
            <label className="mb-1 block text-[12px] font-medium text-slate-600 dark:text-slate-300">Roles</label>
            <div className="mb-1.5 flex gap-1.5">
              <ModeButton active={roleMode === "all"} onClick={() => setRoleMode("all")}>All</ModeButton>
              <ModeButton active={roleMode === "specific"} onClick={() => setRoleMode("specific")}>Specific</ModeButton>
            </div>
            {roleMode === "specific" && (
              <div className="max-h-[140px] overflow-auto rounded-md border border-slate-200 dark:border-slate-800">
                {roles.length === 0 && <p className="p-2 text-[12px] text-slate-400">No roles.</p>}
                {roles.map((r) => (
                  <label key={r.id} className="flex items-center gap-2 border-b border-slate-100 px-2.5 py-1.5 text-[13px] last:border-b-0 dark:border-slate-800">
                    <input type="checkbox" checked={roleSelected.includes(r.name)} onChange={() => setRoleSelected((prev) => toggleIn(prev, r.name))} />
                    {r.name}
                  </label>
                ))}
              </div>
            )}
          </div>
        </div>
        <div>
          <label className="mb-1 block text-[12px] font-medium text-slate-600 dark:text-slate-300">Name (optional)</label>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Shown in the policy list" />
        </div>
        {error && <p className="text-[12px] text-rose-600 dark:text-rose-400">{error}</p>}
        <div className="flex justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            className="inline-flex h-8 items-center rounded-md border border-slate-200 bg-white px-3 text-[13px] font-medium text-slate-600 transition hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800"
          >
            Cancel
          </button>
          <Button disabled={!config.trim() || create.isPending}>
            {create.isPending ? "Saving…" : existing ? "Save changes" : "Create policy"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function PolicyRow({
  policy,
  connName,
  toggleScope,
  busy,
  onToggle,
  onEditScope,
}: {
  policy: Policy;
  connName: Map<string, string>;
  toggleScope: string;
  busy: boolean;
  onToggle: (enabled: boolean, value?: string) => void;
  onEditScope: () => void;
}) {
  const extraActions = useActiveExtensions().flatMap((item) => item.policyActions ?? []);
  const action = inferAction(policy.key, extraActions);
  const extra = extraActions.find((item) => item.value === action);
  const tile = actionTile[action] ?? extra?.tile ?? actionTile.block;
  const icon = actionIcon[action] ?? extra?.icon ?? actionIcon.block;
  const dot = actionDot[action] ?? extra?.dot ?? actionDot.block;
  const title = actionTitle[action] ?? extra?.label ?? action;
  const overrides = policy.overrides ?? [];
  const connOverrides = overrides.filter((o) => o.scope === "connection");
  const roleOverrides = overrides.filter((o) => o.scope === "role");
  const global = policy.globalEnabled ?? policy.enabled;
  return (
    <Panel className="flex items-start gap-3 px-3.5 py-3 transition hover:border-slate-300 dark:hover:border-slate-700">
      <span className={`grid h-9 w-9 shrink-0 place-items-center rounded-md border ${tile}`}>
        <Icon name={icon} size={16} />
      </span>
      <div className="min-w-0 flex-1 py-0.5">
        <div className="flex items-center gap-2">
          <span className="text-[13px] font-medium text-slate-900 dark:text-slate-100">{policy.description}</span>
          <RiskTag risk={policy.risk} />
        </div>
        {policy.detail && (
          <p className="mt-1 max-w-2xl text-[12px] leading-relaxed text-slate-500 dark:text-slate-400">{policy.detail}</p>
        )}
      </div>
      <div className="hidden w-44 shrink-0 flex-wrap items-center gap-1 pt-0.5 md:flex">
        {connOverrides.length === 0 && <ScopeChip label="All" enabled={global} icon="plug" />}
        {connOverrides.map((o) => (
          <ScopeChip key={o.target} label={connName.get(o.target) ?? o.target} enabled={o.enabled} icon="plug" />
        ))}
      </div>
      <div className="hidden w-36 shrink-0 flex-wrap items-center gap-1 pt-0.5 md:flex">
        {roleOverrides.length === 0 && <ScopeChip label="All" enabled={global} icon="users" />}
        {roleOverrides.map((o) => (
          <ScopeChip key={o.target} label={o.target} enabled={o.enabled} icon="users" />
        ))}
      </div>
      <span className="hidden w-20 shrink-0 items-center gap-1.5 pt-1.5 text-[12px] text-slate-500 sm:inline-flex">
        <span className={`h-1.5 w-1.5 rounded-full ${dot}`} />
        {title}
      </span>
      <div className="pt-0.5">
        <Toggle
          on={policy.enabled}
          disabled={busy || (policy.hasValue && !policy.value)}
          onClick={() => onToggle(!policy.enabled, policy.hasValue ? policy.value ?? undefined : undefined)}
          title={
            policy.hasValue && !policy.value
              ? "Set a row count first"
              : `Toggle for ${toggleScope}`
          }
        />
      </div>
      <button
        type="button"
        onClick={onEditScope}
        title="Edit which connections and roles this applies to"
        className="pt-1 text-slate-400 transition hover:text-slate-700 dark:hover:text-slate-200"
      >
        <Icon name="pencil" size={14} />
      </button>
    </Panel>
  );
}

function ScopeChip({ label, enabled, icon }: { label: string; enabled: boolean; icon: IconName }) {
  return (
    <span
      className={`inline-flex h-[18px] items-center gap-1 rounded border px-1.5 text-[10px] font-medium ${
        enabled
          ? "border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-400/20 dark:bg-emerald-500/10 dark:text-emerald-300"
          : "border-rose-200 bg-rose-50 text-rose-700 line-through dark:border-rose-400/20 dark:bg-rose-500/10 dark:text-rose-300"
      }`}
      title={enabled ? "Enabled for this scope" : "Disabled for this scope"}
    >
      <Icon name={icon} size={10} />
      {label}
    </span>
  );
}

const actionTitle: Record<string, string> = { block: "Block", limit: "Limit" };
const actionIcon: Record<string, IconName> = { block: "alert", limit: "table" };
const actionDot: Record<string, string> = {
  block: "bg-rose-500",
  limit: "bg-sky-500",
};
const actionTile: Record<string, string> = {
  block: "border-rose-200 bg-rose-50 text-rose-600 dark:border-rose-400/20 dark:bg-rose-500/10 dark:text-rose-300",
  limit: "border-sky-200 bg-sky-50 text-sky-600 dark:border-sky-400/20 dark:bg-sky-500/10 dark:text-sky-300",
};

function RiskTag({ risk }: { risk: string }) {
  const tone =
    risk === "critical"
      ? "border-rose-200 bg-rose-50 text-rose-700 dark:border-rose-400/20 dark:bg-rose-500/10 dark:text-rose-300"
      : risk === "high"
        ? "border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-400/20 dark:bg-amber-500/10 dark:text-amber-300"
        : "border-slate-200 bg-slate-50 text-slate-500 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-400";
  return <span className={`inline-flex h-[18px] shrink-0 items-center rounded border px-1.5 text-[10px] font-medium capitalize ${tone}`}>{risk}</span>;
}

function Toggle({ on, disabled, onClick, title }: { on: boolean; disabled?: boolean; onClick: () => void; title?: string }) {
  return (
    <button
      onClick={onClick}
      disabled={disabled}
      role="switch"
      aria-checked={on}
      title={title ?? (on ? "Enabled — click to disable" : "Disabled — click to enable")}
      className={`relative h-5 w-9 shrink-0 rounded-full transition disabled:opacity-50 ${on ? "bg-emerald-500" : "bg-slate-300 dark:bg-slate-700"}`}
    >
      <span className={`absolute top-0.5 h-4 w-4 rounded-full bg-white shadow transition-all ${on ? "left-4" : "left-0.5"}`} />
    </button>
  );
}

function inferAction(key: string, extra: PolicyAction[]) {
  if (key === "limit_rows" || key === "query_timeout_seconds") return "limit";
  return extra.find((item) => item.matches(key))?.value ?? "block";
}
