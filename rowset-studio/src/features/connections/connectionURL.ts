import type { Engine, TlsMode } from "./api";

export interface ParsedConnectionURL {
  engine: Engine;
  host: string; port: number; database: string; connectionUsername: string; password: string; tlsMode: TlsMode;
}

const SUPPORTED_OPTIONS = new Set(["sslmode", "ssl", "encrypt", "trustservercertificate", "database", "user", "password", "authsource", "tls"]);

export function parseConnectionURL(raw: string): ParsedConnectionURL {
  const value = raw.trim().replace(/^jdbc:/i, "");
  const url = new URL(value);
  const protocol = url.protocol.toLowerCase();
  const engines: Record<string, ParsedConnectionURL["engine"]> = { "postgres:": "postgres", "postgresql:": "postgres", "mysql:": "mysql", "mariadb:": "mariadb", "sqlserver:": "sqlserver", "mssql:": "sqlserver", "sqlite:": "sqlite", "duckdb:": "duckdb", "clickhouse:": "clickhouse", "mongodb:": "mongodb", "cockroachdb:": "cockroachdb", "cockroach:": "cockroachdb", "redis:": "redis", "valkey:": "valkey", "cassandra:": "cassandra", "elasticsearch:": "elasticsearch" };
  const engine = engines[protocol];
  if (!engine) throw new Error("Use a PostgreSQL, MySQL, MariaDB, SQL Server, SQLite, DuckDB, ClickHouse, MongoDB, CockroachDB, Redis, Cassandra or Elasticsearch URL. SRV URLs are not supported yet.");
  if (url.hash) throw new Error("Encode special characters in the password (for example # as %23).");
  if (engine === "sqlite" || engine === "duckdb") {
    if (url.hostname || url.search || url.hash || url.username || url.password) throw new Error("Use sqlite:///absolute/path or duckdb:///absolute/path without URI options.");
    return { engine, host: "localhost", port: 1, database: decodeURIComponent(url.pathname).replace(/^\/([A-Za-z]:\/)/, "$1"), connectionUsername: "local", password: "", tlsMode: "disable" };
  }
  if (!url.hostname || url.hostname.includes(";")) throw new Error("Use a URL with a host and /database; JDBC semicolon properties are not supported.");
  const defaultPorts: Partial<Record<ParsedConnectionURL["engine"], number>> = { postgres: 5432, cockroachdb: 26257, sqlserver: 1433, clickhouse: 9440, mongodb: 27017, redis: 6379, valkey: 6379, cassandra: 9042, elasticsearch: 9200 };
  const port = Number(url.port || defaultPorts[engine] || 3306);
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error("Invalid database port.");
  // Option names are case-insensitive (SQL Server spells TrustServerCertificate
  // in mixed case); values such as passwords keep their case.
  const options = new Map<string, string>();
  for (const [key, option] of url.searchParams) {
    const name = key.toLowerCase();
    if (!SUPPORTED_OPTIONS.has(name)) throw new Error(`URL option "${key}" is not supported; configure it explicitly before connecting.`);
    if (options.has(name)) throw new Error(`Duplicate URL option "${key}" is ambiguous.`);
    options.set(name, option);
  }
  if (options.has("authsource") && (engine !== "mongodb" || options.get("authsource") !== "admin")) throw new Error("MongoDB authentication currently uses authSource=admin only.");
  if (options.has("tls")) {
    if (engine !== "mongodb" || options.has("ssl")) throw new Error("Use a single MongoDB tls or ssl option.");
    options.set("ssl", options.get("tls")!);
  }
  const tlsMode = urlTLSMode(engine, options);
  return {
    engine, host: url.hostname.replace(/^\[|\]$/g, ""), port: engine === "clickhouse" && !url.port && tlsMode === "disable" ? 9000 : port,
    database: decodeURIComponent(url.pathname.replace(/^\//, "")) || options.get("database") || "",
    connectionUsername: decodeURIComponent(url.username) || options.get("user") || "",
    password: decodeURIComponent(url.password) || options.get("password") || "",
    tlsMode,
  };
}

// Never silently downgrade: a URL asking for verification must keep it, and
// modes that may fall back to plaintext are rejected rather than guessed.
function urlTLSMode(engine: ParsedConnectionURL["engine"], options: Map<string, string>): TlsMode {
  const keys = ["sslmode", "ssl", "encrypt"].filter(key => options.has(key));
  if (keys.length > 1) throw new Error("Use only one TLS option in the URL.");
  const trust = options.get("trustservercertificate")?.toLowerCase();
  if (trust !== undefined && (engine !== "sqlserver" || !["true", "false"].includes(trust))) throw new Error("TrustServerCertificate must be true or false and applies only to SQL Server.");
  const unverified = trust === "true";
  const key = keys[0];
  const mode = key ? options.get(key)!.toLowerCase() : undefined;
  const unsupported = () => new Error("This TLS mode cannot be represented. Choose disable, require, verify-ca or verify-full explicitly.");
  switch (key) {
    case undefined: return unverified ? "require" : "verify-full";
    case "sslmode":
      if (mode === "disable" || mode === "require" || mode === "verify-ca" || mode === "verify-full") return unverified && mode !== "disable" ? "require" : mode;
      throw unsupported();
    case "ssl":
      if (["true", "1", "on"].includes(mode!)) return unverified ? "require" : "verify-full";
      if (["false", "0", "off"].includes(mode!)) return "disable";
      throw unsupported();
    default:
      if (["true", "yes", "mandatory", "1"].includes(mode!)) return unverified ? "require" : "verify-full";
      if (["false", "no", "optional", "0", "disable"].includes(mode!)) return "disable";
      throw unsupported();
  }
}
