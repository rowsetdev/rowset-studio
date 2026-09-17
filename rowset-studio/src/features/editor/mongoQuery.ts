export function mongoQuery(collection = "") {
  return `db.${collection || "collection"}.find({})`;
}

export interface MongoQueryParts {
  collection: string;
  filter: string;
  project: string;
  sort: string;
  skip: string;
  limit: string;
  maxTimeMs: string;
}

// Find the substring between a call's outer parens, tracking nested
// (), {}, [] and quoted strings so commas/braces inside literals don't
// break the split. `source[openIdx]` must be the opening "(".
function extractCall(source: string, openIdx: number): { args: string; endIdx: number } {
  let depth = 0;
  let quote: string | null = null;
  for (let i = openIdx; i < source.length; i++) {
    const ch = source[i];
    if (quote) {
      if (ch === "\\") { i++; continue; }
      if (ch === quote) quote = null;
      continue;
    }
    if (ch === '"' || ch === "'" || ch === "`") { quote = ch; continue; }
    if (ch === "(" || ch === "{" || ch === "[") depth++;
    else if (ch === ")" || ch === "}" || ch === "]") {
      depth--;
      if (depth === 0 && ch === ")") return { args: source.slice(openIdx + 1, i), endIdx: i + 1 };
    }
  }
  throw new Error("Unbalanced parentheses in query.");
}

function splitTopLevelArgs(args: string): string[] {
  const parts: string[] = [];
  let depth = 0, quote: string | null = null, start = 0;
  for (let i = 0; i < args.length; i++) {
    const ch = args[i];
    if (quote) {
      if (ch === "\\") { i++; continue; }
      if (ch === quote) quote = null;
      continue;
    }
    if (ch === '"' || ch === "'" || ch === "`") { quote = ch; continue; }
    if (ch === "(" || ch === "{" || ch === "[") depth++;
    else if (ch === ")" || ch === "}" || ch === "]") depth--;
    else if (ch === "," && depth === 0) { parts.push(args.slice(start, i)); start = i + 1; }
  }
  const last = args.slice(start);
  if (last.trim()) parts.push(last);
  return parts.map(p => p.trim());
}

const SHELL_QUERY = /^\s*db\s*\.\s*([A-Za-z0-9_$]+)\s*\.\s*find\s*\(/;

export function isMongoShellQuery(source: string): boolean {
  return SHELL_QUERY.test(source);
}

// Translate `db.<collection>.find(filter[, projection]).sort(...).limit(n)`
// shell syntax into the {collection,filter,sort,limit} JSON object the
// backend expects, without re-parsing numbers so raw digits survive.
export function mongoShellToRequest(source: string): string {
  const parts = mongoShellToParts(source);
  const fields = [
    `"collection":${JSON.stringify(parts.collection)}`,
    `"filter":${parts.filter || "{}"}`,
    `"sort":${parts.sort || "{}"}`,
    `"limit":${parts.limit || "100"}`,
  ];
  if (parts.project.trim() && parts.project.trim() !== "{}") fields.push(`"project":${parts.project}`);
  if (parts.skip.trim() && parts.skip.trim() !== "0") fields.push(`"skip":${parts.skip}`);
  if (parts.maxTimeMs.trim() && parts.maxTimeMs.trim() !== "0") fields.push(`"maxTimeMs":${parts.maxTimeMs}`);
  return `{${fields.join(",")}}`;
}

// Parse `db.<collection>.find(filter[, projection]).sort(...).skip(...).limit(n)`
// into its parts without reserializing numbers, for the query bar.
export function mongoShellToParts(source: string): MongoQueryParts {
  const match = SHELL_QUERY.exec(source);
  if (!match) throw new Error("Query must start with db.<collection>.find(...)");
  const collection = match[1];
  const openIdx = match[0].length - 1;
  const { args, endIdx } = extractCall(source, openIdx);
  const [filterRaw, projectionRaw] = splitTopLevelArgs(args);
  const filter = (filterRaw ?? "").trim() || "{}";
  const project = (projectionRaw ?? "").trim() || "{}";
  let sort = "{}";
  let skip = "0";
  let limit = "100";
  let maxTimeMs = "0";
  let rest = source.slice(endIdx);
  const chain = /^\s*\.\s*(sort|limit|skip|maxTimeMS|pretty|toArray|count)\s*\(/;
  for (let call = chain.exec(rest); call; call = chain.exec(rest)) {
    const name = call[1];
    const callOpenIdx = call[0].length - 1;
    const { args: callArgs, endIdx: callEndIdx } = extractCall(rest, callOpenIdx);
    if (name === "sort") sort = callArgs.trim() || "{}";
    else if (name === "limit") limit = callArgs.trim() || "100";
    else if (name === "skip") skip = callArgs.trim() || "0";
    else if (name === "maxTimeMS") maxTimeMs = callArgs.trim() || "0";
    rest = rest.slice(callEndIdx);
  }
  if (rest.trim().replace(/;$/, "").trim()) throw new Error(`Unsupported query syntax: ${rest.trim()}`);
  return { collection, filter, project, sort, skip, limit, maxTimeMs };
}

// Splits a flat top-level JSON object's source into {key: rawValueText}
// without JSON.parse, so large numbers keep their exact original digits.
function splitTopLevelObject(source: string): Record<string, string> {
  const trimmed = source.trim();
  if (!trimmed.startsWith("{") || !trimmed.endsWith("}")) throw new Error("Expected a JSON object.");
  const result: Record<string, string> = {};
  for (const part of splitTopLevelArgs(trimmed.slice(1, -1))) {
    const match = /^\s*"((?:[^"\\]|\\.)*)"\s*:\s*([\s\S]*)$/.exec(part);
    if (!match) throw new Error(`Malformed field: ${part}`);
    result[JSON.parse(`"${match[1]}"`)] = match[2].trim();
  }
  return result;
}

// Parse the raw {collection,filter,project,sort,skip,limit,maxTimeMs}
// request object (as saved in Activity/History) into the query bar's parts,
// the same way mongoShellToParts does for db.collection.find() syntax.
export function mongoRequestToParts(source: string): MongoQueryParts {
  const fields = splitTopLevelObject(source);
  const collection = fields.collection ? JSON.parse(fields.collection) : "";
  if (typeof collection !== "string" || !collection.trim()) throw new Error("Query has no collection.");
  return {
    collection,
    filter: fields.filter?.trim() || "{}",
    project: fields.project?.trim() || "{}",
    sort: fields.sort?.trim() || "{}",
    skip: fields.skip?.trim() || "0",
    limit: fields.limit?.trim() || "100",
    maxTimeMs: fields.maxTimeMs?.trim() || "0",
  };
}

// Accepts either shell syntax or the raw request JSON (Activity/History
// entries are saved in the latter), so the query bar can populate itself
// from a query opened from either source.
export function mongoSourceToParts(source: string): MongoQueryParts {
  return isMongoShellQuery(source) ? mongoShellToParts(source) : mongoRequestToParts(source);
}

// Build shell syntax from the query bar's parts, omitting defaults so the
// query reads the way someone would type it by hand.
export function mongoPartsToShell(parts: MongoQueryParts): string {
  const collection = parts.collection.trim() || "collection";
  const filter = parts.filter.trim() || "{}";
  const project = parts.project.trim();
  const args = project && project !== "{}" ? `${filter}, ${project}` : filter;
  let query = `db.${collection}.find(${args})`;
  const sort = parts.sort.trim();
  if (sort && sort !== "{}") query += `.sort(${sort})`;
  const skip = parts.skip.trim();
  if (skip && skip !== "0") query += `.skip(${skip})`;
  const limit = parts.limit.trim();
  if (limit && limit !== "100") query += `.limit(${limit})`;
  const maxTimeMs = parts.maxTimeMs.trim();
  if (maxTimeMs && maxTimeMs !== "0") query += `.maxTimeMS(${maxTimeMs})`;
  return query;
}

// Validate without reserializing the query: large BSON numbers must retain
// their original digits on their way to the server.
export function mongoRequest(source: string, database: string): string {
  const normalized = isMongoShellQuery(source) ? mongoShellToRequest(source) : source;
  const query = JSON.parse(normalized);
  if (!query || Array.isArray(query) || typeof query !== "object") throw new Error("Use a JSON object with collection, filter, project, sort, skip and limit.");
  for (const key of Object.keys(query)) if (!["collection", "filter", "project", "sort", "skip", "limit", "maxTimeMs", "database"].includes(key)) throw new Error(`Unsupported query field: ${key}`);
  if (typeof query.collection !== "string" || !query.collection.trim()) throw new Error("Choose a collection in the query, or open one from the explorer.");
  for (const key of ["filter", "project", "sort"]) if (query[key] != null && (typeof query[key] !== "object" || Array.isArray(query[key]))) throw new Error(`${key} must be a JSON object.`);
  if (query.skip != null && (!Number.isInteger(query.skip) || query.skip < 0)) throw new Error("Skip must be a non-negative integer.");
  if (query.limit != null && (!Number.isInteger(query.limit) || query.limit < 1 || query.limit > 10000)) throw new Error("Document limit must be between 1 and 10000.");
  // 0 is "no limit", the same sentinel the backend and this bar's own
  // Options field use — not an out-of-range value to reject.
  if (query.maxTimeMs != null && (!Number.isInteger(query.maxTimeMs) || query.maxTimeMs < 0 || query.maxTimeMs > 600000)) throw new Error("Max time must be between 0 and 600000 ms.");
  // The toolbar is authoritative, including when replaying history from another database.
  if (query.database != null && query.database !== database) throw new Error("The query database differs from the toolbar. Remove database from the query or select that database.");
  return `{${normalized.trim().slice(1, -1)},"database":${JSON.stringify(database)}}`;
}

const SHELL_AGGREGATE_QUERY = /^\s*db\s*\.\s*([A-Za-z0-9_$]+)\s*\.\s*aggregate\s*\(/;

export function isMongoAggregateShellQuery(source: string): boolean {
  return SHELL_AGGREGATE_QUERY.test(source);
}

// True for shell syntax and for the raw {collection,pipeline,...} request
// object Activity/History save an aggregate() call as (json.Marshal of the
// backend's request struct) — reopening either must route to aggregate(),
// not fall through to find()'s field validation, which rejects "pipeline"
// as an unknown field.
export function isMongoAggregateQuery(source: string): boolean {
  if (isMongoAggregateShellQuery(source)) return true;
  try {
    const parsed: unknown = JSON.parse(source);
    return !!parsed && typeof parsed === "object" && !Array.isArray(parsed) && "pipeline" in parsed;
  } catch {
    return false;
  }
}

export function mongoAggregateQuery(collection = "") {
  return `db.${collection || "collection"}.aggregate([\n  \n])`;
}

export interface MongoAggregateParts {
  collection: string;
  pipeline: string;
}

// Parse `db.<collection>.aggregate([...])` into {collection,pipeline} for
// the query bar, without validating the pipeline itself: that happens on
// run, the same way a malformed find() filter is only caught then.
export function mongoAggregateShellToParts(source: string): MongoAggregateParts {
  const match = SHELL_AGGREGATE_QUERY.exec(source);
  if (!match) throw new Error("Query must start with db.<collection>.aggregate([...])");
  const collection = match[1];
  const openIdx = match[0].length - 1;
  const { args, endIdx } = extractCall(source, openIdx);
  const rest = source.slice(endIdx).trim().replace(/;$/, "").trim();
  if (rest) throw new Error(`Unsupported query syntax: ${rest}`);
  return { collection, pipeline: args.trim() || "[]" };
}

// Parse the raw {collection,pipeline,...} request object into the bar's
// parts, the same way mongoRequestToParts does for find()'s saved shape.
export function mongoAggregateRequestToParts(source: string): MongoAggregateParts {
  const query: unknown = JSON.parse(source);
  if (!query || typeof query !== "object" || Array.isArray(query)) throw new Error("Expected a JSON object.");
  const { collection, pipeline } = query as { collection?: unknown; pipeline?: unknown };
  if (typeof collection !== "string" || !collection.trim()) throw new Error("Query has no collection.");
  return { collection, pipeline: pipeline !== undefined ? JSON.stringify(pipeline, null, 2) : "[]" };
}

// Accepts shell syntax or the raw request object, for the query bar to
// populate itself from either source (typing vs. reopening from
// Activity/History).
export function mongoAggregateSourceToParts(source: string): MongoAggregateParts {
  return isMongoAggregateShellQuery(source) ? mongoAggregateShellToParts(source) : mongoAggregateRequestToParts(source);
}

export function mongoAggregatePartsToShell(parts: MongoAggregateParts): string {
  const collection = parts.collection.trim() || "collection";
  const pipeline = parts.pipeline.trim() || "[]";
  return `db.${collection}.aggregate(${pipeline})`;
}

// Parse `db.<collection>.aggregate([...])` into the {collection,pipeline}
// request object the backend expects. Only the pipeline array itself is
// taken as-is; there is no field-by-field bar for aggregation stages.
export function mongoAggregateShellToRequest(source: string): string {
  const match = SHELL_AGGREGATE_QUERY.exec(source);
  if (!match) throw new Error("Query must start with db.<collection>.aggregate([...])");
  const collection = match[1];
  const openIdx = match[0].length - 1;
  const { args, endIdx } = extractCall(source, openIdx);
  const rest = source.slice(endIdx).trim().replace(/;$/, "").trim();
  if (rest) throw new Error(`Unsupported query syntax: ${rest}`);
  const pipeline = args.trim();
  if (!pipeline.startsWith("[")) throw new Error("aggregate() takes one array of stages.");
  return `{"collection":${JSON.stringify(collection)},"pipeline":${pipeline}}`;
}

// Validate without reserializing, so large BSON numbers in the pipeline
// keep their original digits on their way to the server. Accepts shell
// syntax or the raw request object (Activity/History entries are saved in
// the latter), the same way mongoRequest does for find().
export function mongoAggregateRequest(source: string, database: string): string {
  const normalized = isMongoAggregateShellQuery(source) ? mongoAggregateShellToRequest(source) : source;
  const query = JSON.parse(normalized);
  if (!query || Array.isArray(query) || typeof query !== "object") throw new Error("Use a JSON object with collection and pipeline.");
  for (const key of Object.keys(query)) if (!["collection", "pipeline", "maxTimeMs", "limit", "database"].includes(key)) throw new Error(`Unsupported query field: ${key}`);
  if (typeof query.collection !== "string" || !query.collection.trim()) throw new Error("Choose a collection in the query, or open one from the explorer.");
  if (!Array.isArray(query.pipeline) || query.pipeline.length === 0) throw new Error("The pipeline must be a non-empty array of stages.");
  // 0 is "no limit", the same sentinel find() uses.
  if (query.maxTimeMs != null && (!Number.isInteger(query.maxTimeMs) || query.maxTimeMs < 0 || query.maxTimeMs > 600000)) throw new Error("Max time must be between 0 and 600000 ms.");
  if (query.limit != null && (!Number.isInteger(query.limit) || query.limit < 1 || query.limit > 10000)) throw new Error("Document limit must be between 1 and 10000.");
  if (query.database != null && query.database !== database) throw new Error("The query database differs from the toolbar. Remove database from the query or select that database.");
  return `{${normalized.trim().slice(1, -1)},"database":${JSON.stringify(database)}}`;
}

const UPDATE_SHELL_QUERY = /^\s*db\s*\.\s*([A-Za-z0-9_$]+)\s*\.\s*updateOne\s*\(/;

export function isMongoUpdateShellQuery(source: string): boolean {
  return UPDATE_SHELL_QUERY.test(source);
}

// True for updateOne() shell syntax and for the raw {collection,filter,update,...}
// request object: an "update" field is what tells it apart from a find().
export function isMongoUpdateQuery(source: string): boolean {
  if (isMongoUpdateShellQuery(source)) return true;
  try {
    const parsed: unknown = JSON.parse(source);
    return !!parsed && typeof parsed === "object" && !Array.isArray(parsed) && "update" in parsed;
  } catch {
    return false;
  }
}

const DELETE_SHELL_QUERY = /^\s*db\s*\.\s*([A-Za-z0-9_$]+)\s*\.\s*deleteOne\s*\(/;

export function isMongoDeleteShellQuery(source: string): boolean {
  return DELETE_SHELL_QUERY.test(source);
}

// True for deleteOne() shell syntax and for the raw request object. Unlike
// find()/updateOne(), a delete has {collection,filter} only — the same
// shape as a bare find({...}) — so the raw form needs an explicit
// "delete":true marker to tell the two apart; the marker itself is never
// sent to the backend (see mongoDeleteRequest).
export function isMongoDeleteQuery(source: string): boolean {
  if (isMongoDeleteShellQuery(source)) return true;
  try {
    const parsed: unknown = JSON.parse(source);
    return !!parsed && typeof parsed === "object" && !Array.isArray(parsed) && (parsed as Record<string, unknown>).delete === true;
  } catch {
    return false;
  }
}

export function mongoUpdateQuery(collection = "") {
  return `db.${collection || "collection"}.updateOne({}, {"$set": {}})`;
}

export function mongoDeleteQuery(collection = "") {
  return `db.${collection || "collection"}.deleteOne({})`;
}

function mongoUpdateShellToRequest(source: string): string {
  const match = UPDATE_SHELL_QUERY.exec(source);
  if (!match) throw new Error("Query must start with db.<collection>.updateOne(filter, update)");
  const collection = match[1];
  const openIdx = match[0].length - 1;
  const { args, endIdx } = extractCall(source, openIdx);
  const rest = source.slice(endIdx).trim().replace(/;$/, "").trim();
  if (rest) throw new Error(`Unsupported query syntax: ${rest}`);
  const [filterRaw, updateRaw] = splitTopLevelArgs(args);
  const filter = (filterRaw ?? "").trim() || "{}";
  const update = (updateRaw ?? "").trim();
  if (!update) throw new Error("updateOne(filter, update) needs an update document.");
  return `{"collection":${JSON.stringify(collection)},"filter":${filter},"update":${update}}`;
}

// Validate without reserializing, so large BSON numbers keep their exact
// digits. Accepts updateOne() shell syntax or the raw request object.
// `backup` always comes from the toolbar's own Row backups setting, not
// from the source text, the same way `database` is always the toolbar's.
export function mongoUpdateRequest(source: string, database: string, backup: boolean): string {
  const normalized = isMongoUpdateShellQuery(source) ? mongoUpdateShellToRequest(source) : source;
  const fields = splitTopLevelObject(normalized);
  for (const key of Object.keys(fields)) if (!["collection", "filter", "update", "backup", "database"].includes(key)) throw new Error(`Unsupported query field: ${key}`);
  const collection = fields.collection ? JSON.parse(fields.collection) : undefined;
  if (typeof collection !== "string" || !collection.trim()) throw new Error("Choose a collection in the query, or open one from the explorer.");
  const filter = fields.filter?.trim() || "{}";
  if (!filter.startsWith("{")) throw new Error("filter must be a JSON object.");
  const update = fields.update?.trim();
  if (!update || !update.startsWith("{")) throw new Error('update must be a JSON object, e.g. {"$set": {...}}.');
  if (fields.database != null && JSON.parse(fields.database) !== database) throw new Error("The query database differs from the toolbar. Remove database from the query or select that database.");
  return `{"collection":${JSON.stringify(collection)},"filter":${filter},"update":${update},"backup":${backup ? "true" : "false"},"database":${JSON.stringify(database)}}`;
}

function mongoDeleteShellToRequest(source: string): string {
  const match = DELETE_SHELL_QUERY.exec(source);
  if (!match) throw new Error("Query must start with db.<collection>.deleteOne(filter)");
  const collection = match[1];
  const openIdx = match[0].length - 1;
  const { args, endIdx } = extractCall(source, openIdx);
  const rest = source.slice(endIdx).trim().replace(/;$/, "").trim();
  if (rest) throw new Error(`Unsupported query syntax: ${rest}`);
  const filter = args.trim() || "{}";
  return `{"collection":${JSON.stringify(collection)},"filter":${filter},"delete":true}`;
}

// Validate without reserializing. Accepts deleteOne() shell syntax or the
// raw request object; the "delete":true marker (see isMongoDeleteQuery) is
// dropped here rather than forwarded, since the backend's DELETE endpoint
// doesn't take it and rejects unknown fields.
export function mongoDeleteRequest(source: string, database: string, backup: boolean): string {
  const normalized = isMongoDeleteShellQuery(source) ? mongoDeleteShellToRequest(source) : source;
  const fields = splitTopLevelObject(normalized);
  for (const key of Object.keys(fields)) if (!["collection", "filter", "delete", "backup", "database"].includes(key)) throw new Error(`Unsupported query field: ${key}`);
  const collection = fields.collection ? JSON.parse(fields.collection) : undefined;
  if (typeof collection !== "string" || !collection.trim()) throw new Error("Choose a collection in the query, or open one from the explorer.");
  const filter = fields.filter?.trim() || "{}";
  if (!filter.startsWith("{")) throw new Error("filter must be a JSON object.");
  if (fields.database != null && JSON.parse(fields.database) !== database) throw new Error("The query database differs from the toolbar. Remove database from the query or select that database.");
  return `{"collection":${JSON.stringify(collection)},"filter":${filter},"backup":${backup ? "true" : "false"},"database":${JSON.stringify(database)}}`;
}

// Format punctuation and whitespace without parsing numbers into JS doubles.
export function formatMongoQuery(source: string): string {
  if (isMongoShellQuery(source)) { JSON.parse(mongoShellToRequest(source)); return source.trim(); }
  if (isMongoAggregateShellQuery(source)) { JSON.parse(mongoAggregateShellToRequest(source)); return source.trim(); }
  if (isMongoUpdateShellQuery(source)) { JSON.parse(mongoUpdateShellToRequest(source)); return source.trim(); }
  if (isMongoDeleteShellQuery(source)) { JSON.parse(mongoDeleteShellToRequest(source)); return source.trim(); }
  JSON.parse(source); // Validate syntax only.
  const tokens = source.match(/"(?:[^"\\]|\\.)*"|[^\s]/g) ?? [];
  let depth = 0, result = "";
  const newline = () => { result = result.trimEnd() + "\n" + "  ".repeat(depth); };
  tokens.forEach((token, index) => {
    if (token === "{" || token === "[") {
      result += token;
      if (tokens[index + 1] !== (token === "{" ? "}" : "]")) { depth++; newline(); }
    } else if (token === "}" || token === "]") {
      if (tokens[index - 1] !== (token === "}" ? "{" : "[")) { depth--; newline(); }
      result += token;
    } else if (token === ",") { result += token; newline(); }
    else if (token === ":") result += ": ";
    else result += token;
  });
  return result;
}
