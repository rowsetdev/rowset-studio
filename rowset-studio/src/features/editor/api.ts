import { api, apiResponse, ApiError } from "../../lib/api";
import { readQueryStream, resultAnnotations } from "./queryStream";
import type { MultiRunEvent } from "./multiRun";

export interface QueryResult {
  columns: string[];
  rows: unknown[][];
  rowCount: number;
  durationMs: number;
  truncated?: boolean;
  policyNotice?: string;
  columnTypes?: string[];
  /** Table column behind each result column, null where unknown. */
  columnOrigins?: ({ schema: string; table: string; column: string } | null)[];
  /** Fields the server added on behalf of its extensions. */
  annotations?: Record<string, unknown>;
}

export interface HistoryItem {
  id: string;
  connectionId?: string;
  sql: string;
  status: string;
  rowsReturned: number;
  durationMs: number;
  createdAt: string;
}

export interface ColumnInfo {
  name: string;
  dataType: string;
  nullable: boolean;
  pk?: boolean;
  references?: string; // "schema.table.column" for FK columns
  default?: string;
  generated?: string;
  comment?: string;
}
export interface IndexInfo {
  name: string;
  columns: string[];
  includedColumns?: string[];
  filter?: string;
  unique?: boolean;
  primary?: boolean;
}
export interface TableInfo {
  name: string;
  columns: ColumnInfo[];
  indexes?: IndexInfo[];
}
export interface RoutineInfo {
  name: string;
  kind: string; // "procedure" | "function"
}
export interface TriggerInfo {
  name: string;
  table: string;
  timing: string;
  event: string;
}
export interface SchemaNode {
  name: string;
  tables: TableInfo[];
  views?: TableInfo[];
  routines?: RoutineInfo[];
  triggers?: TriggerInfo[];
  sequences?: { name: string }[];
}
export interface SchemaInfo {
  schemas: SchemaNode[];
  warnings?: string[];
}

// Commands (INSERT/DDL/SET/...) come back as {rowsAffected, durationMs}
// instead of a result set; normalize both shapes into QueryResult.
interface RawQueryResponse extends Partial<QueryResult> {
  rowsAffected?: number;
  durationMs: number;
  error?: string;
}

function normalizeResult(r: RawQueryResponse): QueryResult {
  if (r.error) throw new Error(r.error);
  return {
    columns: r.columns ?? [],
    rows: r.rows ?? [],
    rowCount: r.rowCount ?? r.rowsAffected ?? 0,
    durationMs: r.durationMs,
    truncated: r.truncated ?? false,
    policyNotice: r.policyNotice,
    columnTypes: r.columnTypes,
    columnOrigins: r.columnOrigins ?? undefined,
    annotations: resultAnnotations(r as unknown as Record<string, unknown>),
  };
}

// A 202 means the server held the statement instead of running it.
async function readResult(response: Response, onProgress?: (result: QueryResult) => void): Promise<QueryResult> {
  if (response.status === 202) {
    const body = (await response.json().catch(() => ({}))) as { message?: string };
    throw new ApiError(202, { code: "PENDING", message: body.message ?? "The statement is waiting and has not run." });
  }
  return response.headers.get("Content-Type")?.includes("application/x-ndjson") ? readQueryStream(response, onProgress) : normalizeResult(await response.json());
}

// maxRows 0 asks for every row: only a policy caps a result.
/**
 * Runs statements on several connections through one request; the server
 * runs up to ten at a time and reports each connection's progress as it goes.
 */
export async function runOnConnections(
  request: { targets: { connectionId: string; database: string }[]; statements: string[]; concurrency: number; backup: boolean },
  signal: AbortSignal,
  onEvent: (event: MultiRunEvent) => void,
) {
  const response = await apiResponse("/multirun", { method: "POST", signal, headers: { Accept: "application/x-ndjson" }, body: JSON.stringify(request) });
  if (!response.body) throw new Error("The server sent no progress.");
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let finished = false;
  const accept = (line: string) => {
    if (!line.trim()) return;
    const event = JSON.parse(line) as MultiRunEvent & { result?: RawQueryResponse };
    if (event.type === "done") finished = true;
    onEvent(event.result ? { ...event, result: normalizeResult(event.result) } : event);
  };
  for (;;) {
    const { done, value } = await reader.read();
    buffer += decoder.decode(value, { stream: !done });
    let newline: number;
    while ((newline = buffer.indexOf("\n")) >= 0) {
      accept(buffer.slice(0, newline));
      buffer = buffer.slice(newline + 1);
    }
    if (done) break;
  }
  accept(buffer);
  if (!finished) throw new Error("The run was interrupted before every connection finished.");
}

export async function runQuery(connectionId: string, sql: string, database?: string, nodeRole?: "primary" | "secondary", signal?: AbortSignal, onProgress?: (result: QueryResult) => void, backup = false, maxRows = 0) {
  const response = await apiResponse(`/connections/${connectionId}/query`, {
    method: "POST",
    signal,
    headers: { Accept: "application/x-ndjson" },
    body: JSON.stringify({ sql, database: database ?? "", nodeRole, maxRows, backup }),
  });
  return readResult(response, onProgress);
}

// ---- Manual transactions ----

export function beginTxn(connectionId: string, database?: string) {
  return api<{ txnId: string }>(`/connections/${connectionId}/txn/begin`, {
    method: "POST",
    body: JSON.stringify({ database: database ?? "" }),
  });
}

export async function txnQuery(connectionId: string, txnId: string, sql: string, database?: string, signal?: AbortSignal, onProgress?: (result: QueryResult) => void, backup = false, maxRows = 0) {
  const response = await apiResponse(`/connections/${connectionId}/txn/${txnId}/query`, {
    method: "POST",
    headers: { Accept: "application/x-ndjson" },
    body: JSON.stringify({ sql, database: database ?? "", maxRows, backup }),
    signal,
  });
  return readResult(response, onProgress);
}

export function commitTxn(connectionId: string, txnId: string) {
  return api<void>(`/connections/${connectionId}/txn/${txnId}/commit`, { method: "POST" });
}

export function rollbackTxn(connectionId: string, txnId: string) {
  return api<void>(`/connections/${connectionId}/txn/${txnId}/rollback`, { method: "POST" });
}

export interface PlanResult {
  engine: string;
  format: "json" | "xml" | "text";
  analyzed: boolean;
  plan: string;
}

// Returns the execution plan of one statement. analyze runs a SELECT to
// measure actual rows and timings.
export function explainQuery(connectionId: string, sql: string, options: { database?: string; nodeRole?: "primary" | "secondary"; analyze: boolean; signal?: AbortSignal }) {
  return api<PlanResult>(`/connections/${connectionId}/explain`, {
    method: "POST",
    signal: options.signal,
    body: JSON.stringify({ sql, database: options.database ?? "", nodeRole: options.nodeRole, analyze: options.analyze }),
  });
}

// Downloads a whole table, or the full result of one SELECT; the server
// applies policies as for any SELECT.
export async function exportTable(connectionId: string, request: { database?: string; schema?: string; table?: string; sql?: string; format: "csv" | "json" | "sql" | "cql" }) {
  const response = await apiResponse(`/connections/${connectionId}/export`, { method: "POST", body: JSON.stringify({ ...request, database: request.database ?? "" }) });
  return response.blob();
}

// Document/key writes: MongoDB, Redis/Valkey and Elasticsearch have no SQL,
// so these mirror the grid's UPDATE/DELETE actions with their own shape.
export function mongoInsert(connectionId: string, request: { database?: string; collection: string; document: unknown }) {
  // id is Extended JSON: an object for the common ObjectID case ({"$oid":"..."}),
  // or a plain string/number when the document set its own _id.
  return api<{ id: unknown; durationMs: number }>(`/connections/${connectionId}/documents/insert`, { method: "POST", body: JSON.stringify(request) });
}

export interface BackupNote {
  id: string;
  rows: number;
}
interface WithBackup {
  backup?: BackupNote;
  backupSkipped?: string;
  backupBlocked?: string;
}

export function mongoInsertMany(connectionId: string, request: { database?: string; collection: string; documents: unknown[] }) {
  return api<{ ids: unknown[]; durationMs: number }>(`/connections/${connectionId}/documents/insertMany`, { method: "POST", body: JSON.stringify(request) });
}

export function mongoUpdate(connectionId: string, request: { database?: string; collection: string; filter: unknown; update: unknown; backup?: boolean }) {
  return api<{ matchedCount: number; modifiedCount: number; durationMs: number } & WithBackup>(`/connections/${connectionId}/documents/update`, { method: "POST", body: JSON.stringify(request) });
}

export function mongoDelete(connectionId: string, request: { database?: string; collection: string; filter: unknown; backup?: boolean }) {
  return api<{ deletedCount: number; durationMs: number } & WithBackup>(`/connections/${connectionId}/documents/delete`, { method: "POST", body: JSON.stringify(request) });
}

// Manual-commit MongoDB writes: requires the server to be a replica set or
// mongos; a standalone mongod's rejection surfaces as the begin call's error.
export function mongoBeginTxn(connectionId: string, database?: string) {
  return api<{ txnId: string }>(`/connections/${connectionId}/documents/txn/begin`, { method: "POST", body: JSON.stringify({ database: database ?? "" }) });
}

export function mongoTxnInsert(connectionId: string, txnId: string, request: { collection: string; document: unknown }) {
  return api<{ id: unknown; durationMs: number }>(`/connections/${connectionId}/documents/txn/${txnId}/insert`, { method: "POST", body: JSON.stringify(request) });
}

export function mongoTxnUpdate(connectionId: string, txnId: string, request: { collection: string; filter: unknown; update: unknown; backup?: boolean }) {
  return api<{ matchedCount: number; modifiedCount: number; durationMs: number } & WithBackup>(`/connections/${connectionId}/documents/txn/${txnId}/update`, { method: "POST", body: JSON.stringify(request) });
}

export function mongoTxnDelete(connectionId: string, txnId: string, request: { collection: string; filter: unknown; backup?: boolean }) {
  return api<{ deletedCount: number; durationMs: number } & WithBackup>(`/connections/${connectionId}/documents/txn/${txnId}/delete`, { method: "POST", body: JSON.stringify(request) });
}

export function mongoCommitTxn(connectionId: string, txnId: string) {
  return api<void>(`/connections/${connectionId}/documents/txn/${txnId}/commit`, { method: "POST" });
}

export function mongoRollbackTxn(connectionId: string, txnId: string) {
  return api<void>(`/connections/${connectionId}/documents/txn/${txnId}/rollback`, { method: "POST" });
}

export function redisWrite(connectionId: string, request: { database?: string; key: string; type: "string" | "hash"; field?: string; value: string; ttlSeconds?: number; backup?: boolean }) {
  return api<{ durationMs: number } & WithBackup>(`/connections/${connectionId}/redis/write`, { method: "POST", body: JSON.stringify(request) });
}

export function redisBulkWrite(connectionId: string, request: { database?: string; writes: { key: string; type: "string" | "hash"; field?: string; value: string; ttlSeconds?: number }[] }) {
  return api<{ written: number; durationMs: number }>(`/connections/${connectionId}/redis/bulkWrite`, { method: "POST", body: JSON.stringify(request) });
}

export function redisDelete(connectionId: string, request: { database?: string; key: string; backup?: boolean }) {
  return api<{ deletedCount: number; durationMs: number } & WithBackup>(`/connections/${connectionId}/redis/delete`, { method: "POST", body: JSON.stringify(request) });
}

export function elasticsearchIndex(connectionId: string, request: { index: string; id: string; document: unknown }) {
  return api<{ id: string; durationMs: number }>(`/connections/${connectionId}/elasticsearch/index`, { method: "POST", body: JSON.stringify(request) });
}

export function elasticsearchBulkIndex(connectionId: string, request: { index: string; documents: { id?: string; document: unknown }[] }) {
  return api<{ ids: string[]; errors?: string[]; durationMs: number }>(`/connections/${connectionId}/elasticsearch/bulkIndex`, { method: "POST", body: JSON.stringify(request) });
}

export function elasticsearchUpdate(connectionId: string, request: { index: string; id: string; doc: unknown; backup?: boolean }) {
  return api<{ durationMs: number } & WithBackup>(`/connections/${connectionId}/elasticsearch/update`, { method: "POST", body: JSON.stringify(request) });
}

export function elasticsearchDelete(connectionId: string, request: { index: string; id: string; backup?: boolean }) {
  return api<{ durationMs: number } & WithBackup>(`/connections/${connectionId}/elasticsearch/delete`, { method: "POST", body: JSON.stringify(request) });
}

// The statement that creates an object, for the schema browser's Show DDL.
export function objectDDL(connectionId: string, request: { database?: string; schema?: string; kind: string; name: string }) {
  const query = new URLSearchParams({ kind: request.kind, name: request.name, schema: request.schema ?? "", database: request.database ?? "" });
  return api<{ sql: string }>(`/connections/${connectionId}/ddl?${query}`).then((r) => r.sql);
}

// CSV import: upload the file in chunks, then insert it in one transaction.
export function startImport(connectionId: string) {
  return api<{ importId: string }>(`/connections/${connectionId}/imports`, { method: "POST" });
}

export function uploadImportChunk(connectionId: string, importId: string, chunk: Blob) {
  return api<{ size: number }>(`/connections/${connectionId}/imports/${importId}`, { method: "PUT", body: chunk });
}

export interface ImportRequest {
  schema: string;
  table: string;
  database: string;
  header: boolean;
  delimiter: string;
  nullEmpty: boolean;
  columns: { source: number; target: string }[];
}

export function runImport(connectionId: string, importId: string, request: ImportRequest) {
  return api<{ rows: number; durationMs: number }>(`/connections/${connectionId}/imports/${importId}/run`, { method: "POST", body: JSON.stringify(request) });
}

export function discardImport(connectionId: string, importId: string) {
  return api<void>(`/connections/${connectionId}/imports/${importId}`, { method: "DELETE" });
}

export function listDatabases(connectionId: string) {
  return api<{ databases: string[] | null }>(`/connections/${connectionId}/databases`).then((r) => r.databases ?? []);
}

export function listHistory(connectionId: string, filters?: { from?: string; to?: string }) {
  const qs = new URLSearchParams();
  if (filters?.from) qs.set("from", filters.from);
  if (filters?.to) qs.set("to", filters.to);
  const suffix = qs.size ? `?${qs.toString()}` : "";
  return api<{ history: HistoryItem[] | null }>(`/connections/${connectionId}/history${suffix}`).then(
    (r) => r.history ?? [],
  );
}

// The signed-in user's own statements across all connections (max 200).
export function listMyHistory(filters?: { from?: string; to?: string }) {
  const qs = new URLSearchParams();
  if (filters?.from) qs.set("from", filters.from);
  if (filters?.to) qs.set("to", filters.to);
  const suffix = qs.size ? `?${qs.toString()}` : "";
  return api<{ history: HistoryItem[] | null }>(`/history${suffix}`).then((r) => r.history ?? []);
}

// The server persists the last loaded schema locally; this explicitly drops
// it so the next load reads objects changed outside Rowset.
export function forgetSchema(connectionId: string) {
  return api<void>(`/connections/${connectionId}/schema/refresh`, { method: "POST" });
}

export function getSchema(connectionId: string, database?: string) {
  const qs = database ? `?database=${encodeURIComponent(database)}` : "";
  return api<SchemaInfo>(`/connections/${connectionId}/schema${qs}`).then(normalizeSchema);
}

// Older Rowset versions serialized Go's Index fields as
// Name/Columns/Unique/Primary. Normalize both shapes so cached or
// rolling-upgrade responses cannot crash the explorer.
function normalizeSchema(input: SchemaInfo): SchemaInfo {
  return {
    warnings: input?.warnings ?? [],
    schemas: (input?.schemas ?? []).map((schema) => ({
      ...schema,
      tables: (schema.tables ?? []).map(normalizeTable),
      views: (schema.views ?? []).map(normalizeTable),
      routines: schema.routines ?? [],
      triggers: schema.triggers ?? [],
      sequences: schema.sequences ?? [],
    })),
  };
}

function normalizeTable(table: TableInfo): TableInfo {
  const indexes = (table.indexes ?? []).map((raw) => {
    const legacy = raw as IndexInfo & { Name?: string; Columns?: string[]; Unique?: boolean; Primary?: boolean };
    return {
      name: legacy.name ?? legacy.Name ?? "index",
      columns: legacy.columns ?? legacy.Columns ?? [],
      unique: legacy.unique ?? legacy.Unique ?? false,
      primary: legacy.primary ?? legacy.Primary ?? false,
    };
  });
  return { ...table, columns: table.columns ?? [], indexes };
}

export interface SavedQuery {
  id: string;
  connectionId: string;
  name: string;
  sql: string;
  createdAt: string;
}

export function listSavedQueries() {
  return api<{ queries: SavedQuery[] | null }>("/saved-queries").then((r) => r.queries ?? []);
}

export function saveQuery(input: { connectionId: string | null; name: string; sql: string }) {
  return api<SavedQuery>("/saved-queries", {
    method: "POST",
    body: JSON.stringify({ connectionId: input.connectionId ?? "", name: input.name, sql: input.sql }),
  });
}

export function deleteSavedQuery(id: string) {
  return api<void>(`/saved-queries/${id}`, { method: "DELETE" });
}
