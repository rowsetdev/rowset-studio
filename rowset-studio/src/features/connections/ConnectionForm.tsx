import { FormEvent, useState } from "react";
import { Button, ErrorText, Field, Input, Modal, Select, Textarea } from "../../components/ui";
import { useInstance } from "../../lib/instance";
import { ApiError } from "../../lib/api";
import { Connection, ConnectionInput, Engine, TlsMode, discoverSSHHostKey } from "./api";
import { useCreateConnection, useUpdateConnection } from "./useConnections";
import { useActiveExtensions } from "../../app/extensions";
import { parseConnectionURL } from "./connectionURL";

const DEFAULT_PORT: Record<Engine, number> = {
  postgres: 5432,
  mysql: 3306,
  mariadb: 3306,
  sqlserver: 1433,
  sqlite: 1, duckdb: 1, clickhouse: 9440, mongodb: 27017,
  cockroachdb: 26257, redis: 6379, valkey: 6379, cassandra: 9042, elasticsearch: 9200,
};

const DEFAULT_DATABASE: Record<Engine, string> = {
  postgres: "postgres",
  mysql: "mysql",
  mariadb: "mysql",
  sqlserver: "master",
  sqlite: "", duckdb: "", clickhouse: "default", mongodb: "admin",
  cockroachdb: "defaultdb", redis: "0", valkey: "0", cassandra: "system", elasticsearch: "",
};

export default function ConnectionForm({
  connection,
  onClose,
}: {
  connection?: Connection;
  onClose: () => void;
}) {
  const { data: instance } = useInstance();
  const fields = useActiveExtensions().flatMap((item) => item.connectionFields ?? []);
  const [extra, setExtra] = useState<Record<string, unknown>>({});
  const [connectionURL, setConnectionURL] = useState("");
  const create = useCreateConnection();
  const update = useUpdateConnection();
  const editing = Boolean(connection);
  const [form, setForm] = useState<ConnectionInput>(
    connection
      ? {
          name: connection.name,
          alias: connection.alias,
          engine: (connection.engine as string) === "mssql" ? "sqlserver" : connection.engine,
          host: connection.host,
          port: connection.port,
          database: connection.database,
          environment: connection.environment,
          tlsMode: connection.tlsMode ?? (connection.tlsRequired ? "require" : "disable"),
          tlsServerName: connection.tlsServerName ?? "",
          tlsCaPem: connection.tlsCaPem ?? "",
          tlsClientCertPem: connection.tlsClientCertPem ?? "",
          connectionUsername: connection.connectionUsername,
          password: "",
          queryTimeoutSeconds: connection.queryTimeoutSeconds,
          readOnly: connection.readOnly,
          cassandraConsistency: connection.cassandraConsistency || "QUORUM",
          cassandraPageSize: connection.cassandraPageSize || 1000,
          nodes: connection.nodes.map(({ id, name, host, port }) => ({ id, name, host, port })),
          sshHost: connection.sshHost ?? "",
          sshPort: connection.sshPort || 22,
          sshUser: connection.sshUser ?? "",
          sshAuthMethod: (connection.sshAuthMethod as "password" | "key") || "password",
          sshKnownHost: connection.sshKnownHost ?? "",
        }
      : {
          name: "",
          alias: "",
          engine: "postgres",
          host: "localhost",
          port: 5432,
          database: "postgres",
          environment: "dev",
          tlsMode: "disable",
          tlsServerName: "",
          tlsCaPem: "",
          tlsClientCertPem: "",
          connectionUsername: "",
          password: "",
          queryTimeoutSeconds: 600,
          readOnly: false,
          cassandraConsistency: "QUORUM",
          cassandraPageSize: 1000,
          nodes: [{ name: "node-1", host: "localhost", port: 5432 }],
          sshHost: "",
          sshPort: 22,
          sshUser: "",
          sshAuthMethod: "password",
          sshKnownHost: "",
        },
  );
  const [error, setError] = useState("");
  const fileEngine = form.engine === "sqlite" || form.engine === "duckdb";
  const singleEndpointEngines: Engine[] = ["clickhouse", "mongodb", "redis", "valkey", "elasticsearch"];
  const singleEndpoint = fileEngine || singleEndpointEngines.includes(form.engine);
  const optionalPassword = fileEngine || form.engine === "clickhouse" || form.engine === "mongodb" || form.engine === "redis" || form.engine === "valkey";
  const pending = create.isPending || update.isPending;
  const [sshEnabled, setSshEnabled] = useState(Boolean(connection?.sshHost));
  const [haEnabled, setHaEnabled] = useState(Boolean(connection && connection.nodes.length > 1));
  const [hostKeyState, setHostKeyState] = useState<{ status: "" | "fetching" | "found" | "error"; fingerprint?: string; message?: string }>({ status: "" });

  async function fetchHostKey() {
    setHostKeyState({ status: "fetching" });
    try {
      const result = await discoverSSHHostKey({ sshHost: form.sshHost ?? "", sshPort: form.sshPort, sshUser: form.sshUser, sshAuthMethod: form.sshAuthMethod, sshPassword: form.sshPassword, sshPrivateKey: form.sshPrivateKey, sshPassphrase: form.sshPassphrase });
      if (!result.ok || !result.hostKey) { setHostKeyState({ status: "error", message: result.error ?? "The SSH server could not be reached." }); return; }
      set("sshKnownHost", result.hostKey);
      setHostKeyState({ status: "found", fingerprint: result.fingerprint });
    } catch (err) {
      setHostKeyState({ status: "error", message: err instanceof ApiError ? err.body.message : "The SSH server could not be reached." });
    }
  }

  function set<K extends keyof ConnectionInput>(key: K, value: ConnectionInput[K]) {
    setForm((f) => ({ ...f, [key]: value }));
  }

  function setName(value: string) {
    setForm((f) => ({
      ...f,
      name: value,
      // No separate Alias field to fill in - it's just Name, lowercased and
      // spaced-out, used where a short slug is needed (URLs, the command
      // palette).
      alias: toAlias(value),
    }));
  }

  function onEngineChange(engine: Engine) {
    setForm((f) => ({
      ...f,
      engine,
      connectionUsername: engine === "clickhouse" ? "default" : engine === "mongodb" || engine === "redis" || engine === "valkey" ? "" : f.connectionUsername,
      tlsMode: "disable",
      port: DEFAULT_PORT[engine],
      database: DEFAULT_DATABASE[engine],
      nodes: (engine === "sqlite" || engine === "duckdb" || singleEndpointEngines.includes(engine) ? f.nodes.slice(0, 1) : f.nodes).map((node) => ({ ...node, port: DEFAULT_PORT[engine] })),
    }));
  }

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError("");
    try {
      const first = form.nodes[0];
      const base = first ? { ...form, host: first.host, port: first.port } : { ...form };
      if (!sshEnabled || fileEngine || ["mongodb", "redis", "valkey", "elasticsearch"].includes(form.engine)) { base.sshHost = ""; }
      if (fileEngine) { base.nodes = []; base.tlsMode = "disable"; base.tlsCaPem = ""; base.tlsServerName = ""; base.tlsClientCertPem = ""; base.tlsClientKey = ""; }
      const input = { ...base, ...extra };
      if (connection) await update.mutateAsync({ id: connection.id, input });
      else await create.mutateAsync(input);
      onClose();
    } catch (err) {
      setError(
        err instanceof ApiError
          ? err.body.message
          : `Failed to ${editing ? "update" : "create"} connection`,
      );
    }
  }

  return (
    <Modal
      title={editing ? "Edit connection" : "New connection"}
      onClose={onClose}
      closeOnBackdrop={false}
      showCloseButton={false}
      size="xl"
    >
      <form onSubmit={onSubmit} className="space-y-3">
        {!editing && <div className="flex items-end gap-2">
          <div className="flex-1"><Field label="Paste connection URL (optional)"><Input type="password" autoComplete="off" value={connectionURL} onChange={e => setConnectionURL(e.target.value)} placeholder="postgresql://user:password@host/database" /></Field></div>
          <button type="button" className="rounded border px-3 py-2 text-xs" onClick={() => {
            try {
              const parsed = parseConnectionURL(connectionURL);
              const name = (current: ConnectionInput) => current.name || parsed.database || parsed.host;
              setForm(current => ({ ...current, ...parsed, database: parsed.database || DEFAULT_DATABASE[parsed.engine], name: name(current), alias: toAlias(name(current)), nodes: [{ name: "node-1", host: parsed.host, port: parsed.port }] }));
              setConnectionURL(""); setError("");
            }
            catch (error) { setError(error instanceof Error ? error.message : "Invalid connection URL"); }
          }}>Use URL</button>
        </div>}
        <Field label="Engine">
          <Select value={form.engine} onChange={(e) => onEngineChange(e.target.value as Engine)}>
            <option value="postgres">PostgreSQL</option>
            <option value="mysql">MySQL</option>
            <option value="mariadb">MariaDB</option>
            <option value="sqlserver">SQL Server</option>
            <option value="cockroachdb">CockroachDB</option>
            {instance?.mode === "personal" && <option value="sqlite">SQLite</option>}
            {instance?.mode === "personal" && instance.duckdb && <option value="duckdb">DuckDB</option>}
            {instance?.mode === "personal" && <option value="clickhouse">ClickHouse</option>}
            {instance?.mode === "personal" && <option value="mongodb">MongoDB</option>}
            {instance?.mode === "personal" && <option value="redis">Redis</option>}
            {instance?.mode === "personal" && <option value="valkey">Valkey</option>}
            <option value="cassandra">Cassandra</option>
            {instance?.mode === "personal" && <option value="elasticsearch">Elasticsearch</option>}
          </Select>
        </Field>
        <div className="grid gap-2 md:grid-cols-2">
          <Field label="Name" className="md:col-span-2">
            <Input value={form.name} onChange={(e) => setName(e.target.value)} required />
          </Field>
          {fields.map((ExtraField, index) => <ExtraField key={index} connection={connection} name={form.name} values={extra} onChange={(key, value) => setExtra((current) => ({ ...current, [key]: value }))} />)}
          {/* Username and password side by side - they belong together and are
              what someone reaches for first when filling in a connection. */}
          {!fileEngine && <Field label="Connection username">
            <Input value={form.connectionUsername} onChange={(e) => set("connectionUsername", e.target.value)} required={!optionalPassword} />
          </Field>}
          {!fileEngine && <Field label={editing ? "Password (leave blank to keep current)" : "Password"}>
            <Input
              type="password"
              value={form.password}
              onChange={(e) => set("password", e.target.value)}
              required={!editing && !optionalPassword}
              placeholder={editing ? "••••••••" : undefined}
            />
          </Field>}
          {!fileEngine && (() => {
            const label = form.engine === "clickhouse" ? "Server (native TCP port, usually 9440 with TLS or 9000 without)" : form.engine === "mongodb" ? "Server (authentication database: admin)" : form.engine === "redis" || form.engine === "valkey" ? "Server" : form.engine === "cassandra" ? "Server (native transport port, usually 9042)" : form.engine === "elasticsearch" ? "Server (HTTP API port, usually 9200)" : "Server";
            const first = form.nodes[0];
            const firstStatus = first ? connection?.nodes.find((item) => item.id === first.id) : undefined;
            // A single host/port is the common case for every engine, including
            // ones with several nodes - HA is opt-in instead of always showing
            // a "node name" field and an Add/Remove UI nobody needs yet.
            if (singleEndpoint || !haEnabled) {
              return (
                <Field label={label} className={singleEndpoint ? undefined : "md:col-span-2"}>
                  <div className="space-y-1">
                    <div className="grid grid-cols-3 gap-2">
                      <Input className="col-span-2" value={first?.host ?? ""} placeholder="host" onChange={(e) => set("nodes", [{ ...first, name: first?.name ?? "node-1", host: e.target.value, port: first?.port ?? DEFAULT_PORT[form.engine] }])} required />
                      <Input type="number" value={first?.port ?? DEFAULT_PORT[form.engine]} onChange={(e) => set("nodes", [{ ...first, name: first?.name ?? "node-1", host: first?.host ?? "", port: Number(e.target.value) }])} required />
                    </div>
                    <p className="text-[11px] text-slate-500">{firstStatus ? singleEndpoint ? firstStatus.health : `${firstStatus.health} · ${firstStatus.detectedRole}` : "Connection checked after save"}</p>
                    {!singleEndpoint && (
                      <label className="flex items-center gap-1.5 text-[11px] text-slate-500">
                        <input type="checkbox" checked={haEnabled} onChange={(e) => setHaEnabled(e.target.checked)} />
                        This connection has more than one node (HA / primary-secondary)
                      </label>
                    )}
                  </div>
                </Field>
              );
            }
            return (
              <Field label="HA nodes" className="md:col-span-2">
                <div className="space-y-2">
                  <label className="flex items-center gap-1.5 text-[11px] text-slate-500">
                    <input type="checkbox" checked={haEnabled} onChange={(e) => { setHaEnabled(e.target.checked); if (!e.target.checked) set("nodes", form.nodes.slice(0, 1)); }} />
                    This connection has more than one node (HA / primary-secondary)
                  </label>
                  {form.nodes.map((node, index) => {
                    const status = connection?.nodes.find((item) => item.id === node.id);
                    return <div key={node.id ?? index} className="rounded border border-slate-200 p-2 dark:border-slate-800">
                      <div className="grid grid-cols-6 gap-2">
                        <Input className="col-span-2" value={node.name} placeholder="node name" onChange={(e) => set("nodes", form.nodes.map((item, i) => i === index ? { ...item, name: e.target.value } : item))} required />
                        <Input className="col-span-3" value={node.host} placeholder="host" onChange={(e) => set("nodes", form.nodes.map((item, i) => i === index ? { ...item, host: e.target.value } : item))} required />
                        <Input type="number" value={node.port} onChange={(e) => set("nodes", form.nodes.map((item, i) => i === index ? { ...item, port: Number(e.target.value) } : item))} required />
                      </div>
                      <div className="mt-1 flex items-center gap-2 text-[11px] text-slate-500">
                        <span>{status ? `${status.health} · ${status.detectedRole}` : "role auto-detected after save"}</span>
                        {form.nodes.length > 1 && <button type="button" className="ml-auto text-red-500" onClick={() => set("nodes", form.nodes.filter((_, i) => i !== index))}>Remove</button>}
                      </div>
                    </div>;
                  })}
                  <button type="button" className="text-xs text-orange-600" onClick={() => set("nodes", [...form.nodes, { name: `node-${form.nodes.length + 1}`, host: "", port: DEFAULT_PORT[form.engine] }])}>+ Add node</button>
                  <p className="text-[11px] text-slate-500">Primary/secondary role is detected from the database and refreshed periodically; it is not manually trusted.</p>
                </div>
              </Field>
            );
          })()}
          <Field label={fileEngine ? "Database file (absolute path)" : "Database"}><Input value={form.database} onChange={e => set("database", e.target.value)} placeholder={fileEngine ? "/path/to/database" : DEFAULT_DATABASE[form.engine]} required={fileEngine} /></Field>
          <Field label="Environment">
            <Select value={form.environment} onChange={(e) => set("environment", e.target.value)}>
              <option value="dev">dev</option>
              <option value="preprod">preprod</option>
              <option value="prod">prod</option>
            </Select>
          </Field>
          {!fileEngine && <Field label="TLS">
            <Select value={form.tlsMode} onChange={(e) => set("tlsMode", e.target.value as TlsMode)}>
              <option value="disable">Off</option>
              <option value="require">Encrypt without verification</option>
              <option value="verify-ca">Verify certificate only</option>
              <option value="verify-full">Verify certificate and host name</option>
            </Select>
          </Field>
          }
          <Field label="Safe mode">
            <label className="flex h-8 items-center gap-2 text-[13px] text-slate-700 dark:text-slate-200">
              <input type="checkbox" checked={form.readOnly} onChange={(e) => set("readOnly", e.target.checked)} />
              Read-only: block INSERT, UPDATE, DELETE and DDL
            </label>
          </Field>
          {!fileEngine && (form.tlsMode === "require" || form.tlsMode === "disable") && (
            <p className="text-[11px] text-amber-600 md:col-span-2 md:-mt-1">
              {form.tlsMode === "disable" ? "Credentials and data travel unencrypted." : "Encrypted, but the server identity is not checked: anyone on the network path can impersonate the database."}
            </p>
          )}
          <Field label="Query timeout (minutes)">
            <Input
              type="number"
              min={1}
              max={1440}
              value={Math.round(form.queryTimeoutSeconds / 60)}
              onChange={(e) =>
                set("queryTimeoutSeconds", Math.max(1, Number(e.target.value)) * 60)
              }
              title="Statements that run longer are stopped, with a message saying so. Default 10 minutes, up to 24 hours (1440)."
              required
            />
          </Field>
          {form.engine === "cassandra" && <>
            <Field label="Consistency">
              <Select value={form.cassandraConsistency} onChange={(e) => set("cassandraConsistency", e.target.value)}>
                {['LOCAL_ONE', 'ONE', 'TWO', 'THREE', 'LOCAL_QUORUM', 'QUORUM', 'EACH_QUORUM', 'ALL', 'ANY'].map(value => <option key={value} value={value}>{value}</option>)}
              </Select>
            </Field>
            <Field label="Page size">
              <Input type="number" min={1} max={10000} value={form.cassandraPageSize} onChange={(e) => set("cassandraPageSize", Number(e.target.value))} required />
            </Field>
          </>}
        </div>
        {!fileEngine && form.tlsMode !== "disable" && (
          <details className="rounded border border-slate-200 p-2 dark:border-slate-800" open={Boolean(form.tlsCaPem || form.tlsClientCertPem || form.tlsServerName)}>
            <summary className="cursor-pointer text-xs text-slate-600 dark:text-slate-300">TLS certificates (optional)</summary>
            <div className="mt-2 grid gap-3 md:grid-cols-2">
              <Field label="CA certificate (PEM)">
                <Textarea rows={4} spellCheck={false} value={form.tlsCaPem} onChange={(e) => set("tlsCaPem", e.target.value)} placeholder="-----BEGIN CERTIFICATE----- (empty: operating system trust store)" />
              </Field>
              <Field label="Server name override">
                <div className="space-y-1">
                  <Input value={form.tlsServerName} onChange={(e) => set("tlsServerName", e.target.value)} placeholder="db.internal.example.com" />
                  <p className="text-[11px] text-slate-500 dark:text-slate-400">Name expected in the server certificate when it differs from the node host.</p>
                </div>
              </Field>
              <Field label="Client certificate (PEM)">
                <Textarea rows={4} spellCheck={false} value={form.tlsClientCertPem} onChange={(e) => set("tlsClientCertPem", e.target.value)} placeholder="-----BEGIN CERTIFICATE-----" />
              </Field>
              <Field label="Client private key (PEM)">
                <div className="space-y-1">
                  <Textarea rows={4} spellCheck={false} autoComplete="off" value={form.tlsClientKey ?? ""} onChange={(e) => set("tlsClientKey", e.target.value || undefined)}
                    placeholder={connection?.tlsClientKeyConfigured ? "Stored encrypted. Leave blank to keep it." : "-----BEGIN PRIVATE KEY-----"} />
                  {connection?.tlsClientKeyConfigured && (
                    <label className="flex items-center gap-1 text-[11px] text-slate-500">
                      <input type="checkbox" checked={form.tlsClientKey === ""} onChange={(e) => set("tlsClientKey", e.target.checked ? "" : undefined)} />
                      Remove stored key
                    </label>
                  )}
                </div>
              </Field>
            </div>
          </details>
        )}
        {!fileEngine && <details className="rounded border border-slate-200 p-2 dark:border-slate-800" open={sshEnabled}>
          <summary className="cursor-pointer text-xs text-slate-600 dark:text-slate-300">SSH tunnel (optional)</summary>
          <div className="mt-2 space-y-3">
            <label className="flex items-center gap-2 text-[12px] text-slate-600 dark:text-slate-300">
              <input type="checkbox" checked={sshEnabled} onChange={(e) => { setSshEnabled(e.target.checked); setHostKeyState({ status: "" }); }} />
              Reach the database through an SSH server
            </label>
            {sshEnabled && (
              <div className="space-y-3">
                <div className="grid gap-3 md:grid-cols-3">
                  <Field label="SSH host"><Input value={form.sshHost ?? ""} onChange={(e) => { set("sshHost", e.target.value); setHostKeyState({ status: "" }); }} placeholder="bastion.example.com" /></Field>
                  <Field label="SSH port"><Input type="number" min={1} max={65535} value={form.sshPort ?? 22} onChange={(e) => set("sshPort", Number(e.target.value) || 22)} /></Field>
                  <Field label="SSH user"><Input value={form.sshUser ?? ""} onChange={(e) => set("sshUser", e.target.value)} placeholder="ubuntu" /></Field>
                </div>
                <Field label="Authentication">
                  <Select value={form.sshAuthMethod ?? "password"} onChange={(e) => set("sshAuthMethod", e.target.value as "password" | "key")}>
                    <option value="password">Password</option>
                    <option value="key">Private key</option>
                  </Select>
                </Field>
                {form.sshAuthMethod === "key" ? (
                  <div className="grid gap-3 md:grid-cols-2">
                    <Field label="Private key (PEM)">
                      <Textarea rows={4} spellCheck={false} autoComplete="off" value={form.sshPrivateKey ?? ""} onChange={(e) => set("sshPrivateKey", e.target.value || undefined)}
                        placeholder={connection?.sshConfigured ? "Stored encrypted. Leave blank to keep it." : "-----BEGIN OPENSSH PRIVATE KEY-----"} />
                    </Field>
                    <Field label="Key passphrase (optional)"><Input type="password" autoComplete="off" value={form.sshPassphrase ?? ""} onChange={(e) => set("sshPassphrase", e.target.value || undefined)} placeholder={connection?.sshConfigured ? "Leave blank to keep" : ""} /></Field>
                  </div>
                ) : (
                  <Field label={connection?.sshConfigured ? "SSH password (leave blank to keep current)" : "SSH password"}>
                    <Input type="password" autoComplete="off" value={form.sshPassword ?? ""} onChange={(e) => set("sshPassword", e.target.value || undefined)} />
                  </Field>
                )}
                <div className="rounded border border-slate-200 p-2 dark:border-slate-800">
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-[12px] font-medium text-slate-700 dark:text-slate-200">Host key</span>
                    <button type="button" onClick={() => void fetchHostKey()} disabled={hostKeyState.status === "fetching" || !form.sshHost} className="rounded border px-2.5 py-1 text-[11px] disabled:opacity-50">
                      {hostKeyState.status === "fetching" ? "Fetching…" : "Fetch host key"}
                    </button>
                  </div>
                  {form.sshKnownHost ? (
                    <p className="mt-1 break-all text-[11px] text-emerald-600 dark:text-emerald-400">
                      Trusted{hostKeyState.fingerprint ? `: ${hostKeyState.fingerprint}` : ""}. The tunnel is refused if the server's key ever changes.
                    </p>
                  ) : (
                    <p className="mt-1 text-[11px] text-amber-600 dark:text-amber-400">
                      The host key is not trusted yet. Fetch it and check the fingerprint before saving; a connection cannot open without it.
                    </p>
                  )}
                  {hostKeyState.status === "error" && <p className="mt-1 text-[11px] text-rose-600 dark:text-rose-400">{hostKeyState.message}</p>}
                </div>
              </div>
            )}
          </div>
        </details>}
        {fileEngine && <p className="text-[12px] text-slate-500">Open an existing file on this computer. SQLite and DuckDB do not use network credentials. DuckDB requires a build with DuckDB support.</p>}
        {form.engine === "mongodb" && <p className="text-[12px] text-slate-500">Browse collections, find documents and run read-only aggregation pipelines using Extended JSON, with single-document insert/update/delete. Manual-commit transactions need a replica set or mongos — a standalone server rejects them with a clear error. SRV URLs are not supported yet. Leave the username blank for an unauthenticated local server.</p>}
        {form.engine === "cockroachdb" && <p className="text-[12px] text-slate-500">CockroachDB is queried through its PostgreSQL wire protocol; SQL, schema browsing and HA nodes all work the same way they do for PostgreSQL.</p>}
        {form.engine === "redis" && <p className="text-[12px] text-slate-500">Browse keys by pattern (string, hash, list, set, zset, stream). The Database field is a numeric index (0-15). String/hash key writes and delete are supported. Leave the username blank for an unauthenticated server.</p>}
        {form.engine === "valkey" && <p className="text-[12px] text-slate-500">Valkey speaks the same protocol as Redis, so it works the same way here: browse keys by pattern, the Database field is a numeric index (0-15), string/hash key writes and delete are supported. Leave the username blank for an unauthenticated server.</p>}
        {form.engine === "cassandra" && <p className="text-[12px] text-slate-500">Runs policy-governed CQL reads, writes, DDL and batches. Add contact points with HA nodes; SSH, TLS, consistency and paging are supported.</p>}
        {form.engine === "elasticsearch" && <p className="text-[12px] text-slate-500">Searches one index at a time with a JSON query body and supports single-document index/update/delete. Aggregations across indices are not supported yet.</p>}
        <ErrorText>{error}</ErrorText>
        <div className="flex justify-end gap-2 pt-2">
          <button type="button" onClick={onClose} className="px-4 py-2 text-sm text-slate-400 hover:text-slate-100">
            Cancel
          </button>
          <Button type="submit" disabled={pending}>
            {pending ? (editing ? "Saving…" : "Creating…") : editing ? "Save changes" : "Create"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function toAlias(value: string) {
  return value
    .toLowerCase()
    .trim()
    .replace(/[\s_.]+/g, "-")
    .replace(/[^a-z0-9-]/g, "")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 63);
}
