import { api } from "../../lib/api";

export type Engine = "postgres" | "mysql" | "mariadb" | "sqlserver" | "sqlite" | "duckdb" | "clickhouse" | "mongodb" | "cockroachdb" | "redis" | "valkey" | "cassandra" | "elasticsearch";

/** libpq semantics on every engine: require encrypts without verifying the server. */
export type TlsMode = "disable" | "require" | "verify-ca" | "verify-full";

export interface Connection {
  id: string;
  name: string;
  alias: string;
  engine: Engine;
  host: string;
  port: number;
  database: string;
  environment: string;
  tlsRequired: boolean;
  tlsMode: TlsMode;
  tlsServerName: string;
  tlsCaPem: string;
  tlsClientCertPem: string;
  tlsClientKeyConfigured: boolean;
  connectionUsername: string;
  createdAt: string;
  queryTimeoutSeconds: number;
  /** Blocks every write statement on this connection, regardless of role. */
  readOnly: boolean;
  cassandraConsistency: string;
  cassandraPageSize: number;
  nodes: ConnectionNode[];
  nodePolicy?: "primary_only" | "secondary_only" | "user_selectable";
  defaultNodeRole?: "primary" | "secondary";
  sshHost?: string;
  sshPort?: number;
  sshUser?: string;
  sshAuthMethod?: string;
  sshKnownHost?: string;
  sshConfigured?: boolean;
}

export interface ConnectionNode {
  id: string;
  name: string;
  host: string;
  port: number;
  detectedRole: "primary" | "secondary" | "unknown";
  health: "healthy" | "unreachable" | "unknown";
  readOnly: boolean;
  lastCheckedAt: string | null;
  lastError: string | null;
}

export interface ConnectionNodeInput { id?: string; name: string; host: string; port: number }

export interface ConnectionInput {
  name: string;
  alias: string;
  engine: Engine;
  host: string;
  port: number;
  database: string;
  environment: string;
  tlsMode: TlsMode;
  tlsServerName: string;
  tlsCaPem: string;
  tlsClientCertPem: string;
  /** Write-only. Omit to keep the stored key; "" removes it. */
  tlsClientKey?: string;
  connectionUsername: string;
  password: string;
  queryTimeoutSeconds: number;
  readOnly: boolean;
  cassandraConsistency: string;
  cassandraPageSize: number;
  nodes: ConnectionNodeInput[];
  // SSH tunnel. sshHost "" turns it off. The credentials are write-only.
  sshHost?: string;
  sshPort?: number;
  sshUser?: string;
  sshAuthMethod?: "password" | "key";
  sshKnownHost?: string;
  sshPassword?: string;
  sshPrivateKey?: string;
  sshPassphrase?: string;
}

export interface TestResult {
  ok: boolean;
  latencyMs: number;
  error?: string;
}

export function listConnections() {
  return api<{ connections: Connection[] | null }>("/connections").then(
    (r) => r.connections ?? [],
  );
}

export function createConnection(input: ConnectionInput) {
  return api<Connection>("/connections", { method: "POST", body: JSON.stringify(input) });
}

export function updateConnection(id: string, input: ConnectionInput) {
  return api<Connection>(`/connections/${id}`, { method: "PUT", body: JSON.stringify(input) });
}

export interface SSHHostKeyResult {
  ok: boolean;
  hostKey?: string;
  fingerprint?: string;
  error?: string;
}

export function discoverSSHHostKey(input: { sshHost: string; sshPort?: number; sshUser?: string; sshAuthMethod?: string; sshPassword?: string; sshPrivateKey?: string; sshPassphrase?: string }) {
  return api<SSHHostKeyResult>("/connections/ssh/host-key", { method: "POST", body: JSON.stringify(input) });
}

export function deleteConnection(id: string) {
  return api<void>(`/connections/${id}`, { method: "DELETE" });
}

export function testConnection(id: string) {
  return api<TestResult>(`/connections/${id}/test`, { method: "POST" });
}
