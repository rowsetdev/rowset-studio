// Running what the console was given. A meta command resolves through
// consoleMeta; anything else goes to the very endpoint the editor uses, so a
// statement typed here gets the same policy check, row backup and audit entry
// it would get there. Nothing in the console talks to a database directly.
import { redisDelete, redisWrite, runQuery, type QueryResult } from "../editor/api";
import { runCassandra, runElasticsearch, runMongo, runRedis } from "../editor/nosqlRun";
import type { ConsoleBlock } from "./consoleFormat";
import { rowCountText } from "./consoleFormat";
import { matchCommand } from "./consoleEngines";
import { metaActions, type MetaContext } from "./consoleMeta";
import { redisPlan } from "./consoleRedis";
import { elasticsearchBody } from "./consoleStatement";

export interface RunContext extends MetaContext {
  connectionId: string;
  backup: boolean;
  signal?: AbortSignal;
  /** Reported while rows are still arriving. */
  onProgress?: (result: QueryResult) => void;
}

function resultBlocks(result: QueryResult, verb: string): ConsoleBlock[] {
  const notice = result.policyNotice;
  if (!result.columns.length) {
    return [{ kind: "affected", text: `${verb} ${rowCountText(result.rowCount)}`, durationMs: result.durationMs }, ...(notice ? [{ kind: "notice" as const, text: notice }] : [])];
  }
  // One column of documents is JSON, not a table: printing a 40-field object
  // in a cell hides it, so the document engines get their own block.
  if (result.columns.length === 1 && (result.columnTypes?.[0] === "BSON" || result.columnTypes?.[0] === "JSON")) {
    return [{ kind: "documents", documents: result.rows.map(row => row[0]), durationMs: result.durationMs, truncated: result.truncated, notice }];
  }
  return [{ kind: "result", columns: result.columns, rows: result.rows, rowCount: result.rowCount, durationMs: result.durationMs, truncated: result.truncated, notice }];
}

async function runRedisLine(context: RunContext, statement: string): Promise<ConsoleBlock[]> {
  const plan = redisPlan(statement);
  if (plan.kind === "error") return [{ kind: "error", text: plan.message }];
  if (plan.kind === "delete") {
    const response = await redisDelete(context.connectionId, { database: context.database, key: plan.key, backup: context.backup });
    return [{ kind: "affected", text: response.deletedCount === 1 ? "Deleted 1 key" : `Deleted ${response.deletedCount} keys`, durationMs: response.durationMs }];
  }
  if (plan.kind === "write") {
    const response = await redisWrite(context.connectionId, { database: context.database, key: plan.key, type: plan.type, field: plan.field, value: plan.value, ttlSeconds: plan.ttlSeconds, backup: context.backup });
    return [{ kind: "affected", text: "OK", durationMs: response.durationMs }];
  }
  const result = await runRedis({ connectionId: context.connectionId, source: JSON.stringify({ pattern: plan.pattern, type: plan.type, limit: plan.limit }), database: context.database, backup: context.backup, signal: context.signal });
  if (!plan.columns) return resultBlocks(result, "Scanned");
  // GET, TYPE and TTL each ask about one key; showing the other three
  // columns of a scan would bury the answer.
  const keep = plan.columns.map(name => result.columns.indexOf(name)).filter(index => index >= 0);
  return [{ kind: "result", columns: keep.map(index => result.columns[index]), rows: result.rows.map(row => keep.map(index => row[index])), rowCount: result.rowCount, durationMs: result.durationMs, truncated: result.truncated, notice: result.policyNotice }];
}

/** Runs one whole statement, or one meta command, and returns what to print. */
export async function runLine(context: RunContext, statement: string): Promise<ConsoleBlock[]> {
  const matched = matchCommand(context.spec, statement);
  if (matched) {
    const action = metaActions[matched.command.action];
    if (!action) return [{ kind: "error", text: `${matched.command.name} is not implemented.` }];
    return action({ ...context, argument: matched.argument, option: matched.command.option });
  }
  const shared = { connectionId: context.connectionId, source: statement, database: context.database, backup: context.backup, signal: context.signal };
  switch (context.spec.kind) {
    case "mongo":
      return resultBlocks(await runMongo(shared), "Changed");
    case "cassandra":
      return resultBlocks(await runCassandra(shared), "Applied");
    case "elasticsearch":
      return resultBlocks(await runElasticsearch({ ...shared, source: elasticsearchBody(statement, context.database) }), "Indexed");
    case "redis":
      return runRedisLine(context, statement);
    default: {
      const result = await runQuery(context.connectionId, statement, context.database, undefined, context.signal, context.onProgress, context.backup);
      return resultBlocks(result, "Affected");
    }
  }
}
