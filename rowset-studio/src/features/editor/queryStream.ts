export interface StreamResult {
  columns: string[]; columnTypes?: string[]; rows: unknown[][]; rowCount: number;
  durationMs: number; truncated?: boolean; policyNotice?: string;
  /** Table column behind each result column, null where unknown. */
  columnOrigins?: ({ schema: string; table: string; column: string } | null)[];
  /** Fields the server added on behalf of its extensions. */
  annotations?: Record<string, unknown>;
  /**
   * The results a batch returned after the first. One statement returns one
   * result; a batch of several SELECTs returns one each, and a client that
   * read only the first would show part of what it ran.
   */
  more?: StreamResult[];
}

const RESULT_FIELDS = new Set(["type", "columns", "columnTypes", "columnOrigins", "rows", "rowCount", "rowsAffected", "durationMs", "truncated", "policyNotice", "error", "status", "message", "more"]);

/** Returns the fields of a result message that the result format does not define. */
export function resultAnnotations(source: Record<string, unknown>): Record<string, unknown> {
  const annotations: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(source)) if (!RESULT_FIELDS.has(key)) annotations[key] = value;
  return annotations;
}

// Long results report progress less often, so rendering never costs more
// than receiving.
function progressInterval(rows: number) {
  return rows > 200_000 ? 2000 : rows > 20_000 ? 500 : 100;
}

export async function readQueryStream(response: Response, onProgress?: (result: StreamResult) => void): Promise<StreamResult> {
  if (!response.body) throw new Error("Missing query response body");
  const first: StreamResult = { columns: [], rows: [], rowCount: 0, durationMs: 0 };
  // Every result the batch returned, in order. "current" is the one being
  // read; the first is what callers that know nothing of batches still get.
  const all: StreamResult[] = [first];
  let current = first;
  const reader = response.body.getReader(), decoder = new TextDecoder();
  let buffer = "", complete = false, receivedColumns = false;
  let lastProgress = 0;
  function startResult(event: { columns?: unknown; columnTypes?: string[]; columnOrigins?: StreamResult["columnOrigins"] }, target: StreamResult) {
    if (!Array.isArray(event.columns) || !event.columns.every((column: unknown) => typeof column === "string")) throw new Error("Invalid query metadata");
    target.columns = event.columns as string[];
    target.columnTypes = event.columnTypes;
    target.columnOrigins = event.columnOrigins ?? undefined;
  }
  function accept(line: string) {
    if (!line.trim()) return;
    const event = JSON.parse(line);
    if (event.type === "columns") {
      if (receivedColumns) throw new Error("Invalid query metadata");
      receivedColumns = true;
      startResult(event, first);
      first.annotations = resultAnnotations(event);
    } else if (event.type === "result") {
      // A further result of the same batch.
      if (!receivedColumns || !complete) throw new Error("Invalid query metadata");
      complete = false;
      current = { columns: [], rows: [], rowCount: 0, durationMs: 0 };
      startResult(event, current);
      all.push(current);
    } else if (event.type === "rows") {
      if (!receivedColumns || complete || !Array.isArray(event.rows) || !event.rows.every((row: unknown) => Array.isArray(row) && row.length === current.columns.length)) throw new Error("Invalid query row batch");
      for (const row of event.rows) current.rows.push(row);
      current.rowCount = current.rows.length;
      // Report progress without copying the rows: copying them on every
      // update would cost more the longer a large result streams. The grid
      // reads the growing array and re-renders on the new wrapper object.
      if (Date.now() - lastProgress > progressInterval(current.rowCount)) { onProgress?.({ ...current }); lastProgress = Date.now(); }
    } else if (event.type === "complete") {
      if (complete) throw new Error("Unexpected data after query completion");
      complete = true;
      if (event.error) throw new Error(event.error);
      if (!receivedColumns || event.rowCount !== current.rows.length || !Number.isFinite(event.durationMs) || event.durationMs < 0) throw new Error("Invalid query completion metadata");
      current.rowCount = event.rowCount; current.durationMs = event.durationMs;
      current.truncated = event.truncated; current.policyNotice = event.policyNotice;
    } else throw new Error("Unknown query stream event");
  }
  try {
    while (true) {
      const { done, value } = await reader.read();
      buffer += decoder.decode(value, { stream: !done });
      let newline: number;
      while ((newline = buffer.indexOf("\n")) >= 0) { accept(buffer.slice(0, newline)); buffer = buffer.slice(newline + 1); }
      if (done) break;
    }
    if (buffer.trim()) accept(buffer);
    if (!complete || !receivedColumns) throw new Error("Query response interrupted before completion");
    if (all.length > 1) first.more = all.slice(1);
    return first;
  } finally { await reader.cancel().catch(() => undefined); reader.releaseLock(); }
}
