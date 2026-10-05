import { api } from "../../lib/api";
import { mongoAggregateRequest, mongoDeleteRequest, mongoRequest, mongoUpdateRequest, isMongoAggregateQuery, isMongoDeleteQuery, isMongoUpdateQuery } from "./mongoQuery";
import { mongoDelete, mongoTxnDelete, mongoTxnUpdate, mongoUpdate, type QueryResult } from "./api";

// Running a query against a document, key-value or CQL engine is not one
// request like SQL is: each engine has its own endpoints, and the response
// has to be shaped into the QueryResult every result view expects. These
// functions hold that per-engine shaping so the editor and the console run
// the same code instead of each keeping its own copy.
export interface NoSqlRun {
  connectionId: string;
  /** The query as typed: shell syntax for MongoDB, JSON for the others. */
  source: string;
  /** Database, keyspace or Redis index, as the engine calls it. */
  database: string;
  backup: boolean;
  signal?: AbortSignal;
  /** An open MongoDB transaction, for writes that belong inside it. */
  txnId?: string;
}

interface BackupFields {
  backup?: { id: string; rows: number };
  backupSkipped?: string;
}

export function backupNotice(result: BackupFields): string | undefined {
  if (result.backup) return `Backed up ${result.backup.rows} document(s); restore from Activity → Row backups.`;
  if (result.backupSkipped) return `No backup was taken: ${result.backupSkipped}.`;
  return undefined;
}

export async function runMongo({ connectionId, source, database, backup, signal, txnId }: NoSqlRun): Promise<QueryResult> {
  const aggregate = isMongoAggregateQuery(source);
  if (!aggregate && isMongoUpdateQuery(source)) {
    const request = JSON.parse(mongoUpdateRequest(source, database, backup)) as { collection: string; filter: unknown; update: unknown; backup: boolean; database: string };
    const response = txnId
      ? await mongoTxnUpdate(connectionId, txnId, { collection: request.collection, filter: request.filter, update: request.update, backup: request.backup })
      : await mongoUpdate(connectionId, request);
    return { columns: ["matchedCount", "modifiedCount"], rows: [[response.matchedCount, response.modifiedCount]], rowCount: 1, durationMs: response.durationMs, policyNotice: backupNotice(response) };
  }
  if (!aggregate && isMongoDeleteQuery(source)) {
    const request = JSON.parse(mongoDeleteRequest(source, database, backup)) as { collection: string; filter: unknown; backup: boolean; database: string };
    const response = txnId
      ? await mongoTxnDelete(connectionId, txnId, { collection: request.collection, filter: request.filter, backup: request.backup })
      : await mongoDelete(connectionId, request);
    return { columns: ["deletedCount"], rows: [[response.deletedCount]], rowCount: 1, durationMs: response.durationMs, policyNotice: backupNotice(response) };
  }
  const path = aggregate ? "aggregate" : "find";
  const body = aggregate ? mongoAggregateRequest(source, database) : mongoRequest(source, database);
  const response = await api<{ documents: unknown[]; truncated: boolean; durationMs: number; limit: number }>(`/connections/${connectionId}/documents/${path}`, { method: "POST", signal, body });
  return { columns: ["document"], columnTypes: ["BSON"], rows: response.documents.map(document => [document]), rowCount: response.documents.length, durationMs: response.durationMs, truncated: response.truncated, policyNotice: response.truncated ? `Document limit: ${response.limit}` : undefined };
}

export async function runElasticsearch({ connectionId, source, signal }: NoSqlRun): Promise<QueryResult> {
  const response = await api<{ documents: unknown[]; truncated: boolean; aggregations?: unknown; searchAfter?: unknown; durationMs: number; limit: number }>(`/connections/${connectionId}/elasticsearch/search`, { method: "POST", signal, body: source });
  // Aggregations and the sort values for the next search_after page have
  // no column of their own, so they show as one leading pseudo-document.
  const meta = response.aggregations !== undefined || response.searchAfter !== undefined
    ? [{ _aggregations: response.aggregations, _searchAfter: response.searchAfter }]
    : [];
  const documents = [...meta, ...response.documents];
  return { columns: ["document"], columnTypes: ["JSON"], rows: documents.map(document => [document]), rowCount: documents.length, durationMs: response.durationMs, truncated: response.truncated, policyNotice: response.truncated ? `Result size: ${response.limit}` : undefined };
}

export async function runRedis({ connectionId, source, database, signal }: NoSqlRun): Promise<QueryResult> {
  const input = JSON.parse(source) as { pattern?: string; type?: string; limit?: number };
  const body = JSON.stringify({ database, pattern: input.pattern, type: input.type, limit: input.limit });
  const response = await api<{ entries: { key: string; type: string; ttl: number; value: unknown }[]; cursor: number; durationMs: number; limit: number }>(`/connections/${connectionId}/redis/scan`, { method: "POST", signal, body });
  return { columns: ["key", "type", "ttl", "value"], rows: response.entries.map(e => [e.key, e.type, e.ttl, e.value]), rowCount: response.entries.length, durationMs: response.durationMs, truncated: response.cursor !== 0, policyNotice: response.cursor !== 0 ? `Scan limit: ${response.limit}; more keys remain (cursor ${response.cursor})` : undefined };
}

export async function runCassandra({ connectionId, source, database, backup, signal }: NoSqlRun): Promise<QueryResult> {
  const response = await api<{ columns: string[]; rows: unknown[][]; durationMs: number; truncated: boolean } & BackupFields>(`/connections/${connectionId}/cassandra/query`, { method: "POST", signal, body: JSON.stringify({ keyspace: database, query: source, limit: 1000, backup }) });
  return { columns: response.columns ?? [], rows: response.rows ?? [], rowCount: (response.rows ?? []).length, durationMs: response.durationMs, truncated: response.truncated, policyNotice: backupNotice(response) };
}
