import type { SchemaInfo } from "./api";
import { hashComments, sqlTokens } from "./sqlText.ts";

/** One foreign key, in the direction it is written. */
export interface ForeignKey {
  /** Column of the table this key belongs to. */
  column: string;
  /** Table it points at, as schema.table lower-case. */
  table: string;
  toColumn: string;
}

export interface SqlCompletions {
  tableColumns: Record<string, string[]>;
  tableNames: Record<string, string>;
  schemaTables: Record<string, string[]>;
  schemaNames: Record<string, string>;
  routines: { name: string; kind: string }[];
  /** Keys of each table, both the ones it declares and the ones pointing at it. */
  foreignKeys?: Record<string, ForeignKey[]>;
  /** Databases a statement can name, keyed by lower-case name. */
  databaseNames?: Record<string, string>;
  /** Schemas and objects already loaded for cross-database completion. */
  databaseSchemaNames?: Record<string, Record<string, string>>;
  databaseSchemaTables?: Record<string, Record<string, string[]>>;
  databaseTables?: Record<string, string[]>;
  databaseTableColumns?: Record<string, string[]>;
  /** Engine of the connection, lower-case, when known. */
  engine?: string;
}

// Engines where one statement can reach another database by name.
const CROSS_DATABASE = new Set(["mysql", "mariadb", "mssql", "sqlserver", "cockroachdb", "clickhouse", "cassandra"]);
// These engines address an object as database.table (or keyspace.table)
// instead of database.schema.table.
const DIRECT_DATABASE = new Set(["mysql", "mariadb", "clickhouse", "cassandra"]);
const QUOTED_SQL_ENGINES = new Set(["postgres", "postgresql", "cockroachdb", "sqlite", "duckdb", "clickhouse", "cassandra"]);

const STOP_WORDS = new Set([
  "where", "on", "join", "inner", "left", "right", "full", "cross", "group",
  "order", "having", "limit", "offset", "union", "set", "values", "as", "using",
]);

export function aliasMap(sql: string): Record<string, string> {
  const map: Record<string, string> = {};
  const re = /\b(?:from|join|update|into)\s+(?:(["`[]?[\w$]+["`\]]?)\.)?(?:(["`[]?[\w$]+["`\]]?)\.)?(["`[]?[\w$]+["`\]]?)(?:\s+(?:as\s+)?([a-z_][\w$]*))?/gi;
  const clean = (value: string) => value.replace(/^["`[]|["`\]]$/g, "").toLowerCase();
  for (const match of sql.matchAll(re)) {
    const prefix = [match[1], match[2]].filter(Boolean).map((part) => clean(part!));
    const table = clean(match[3]);
    const qualified = [...prefix, table].join(".");
    map[table] = qualified;
    map[qualified] = qualified;
    const alias = match[4]?.toLowerCase() ?? "";
    if (alias && !STOP_WORDS.has(alias)) map[alias] = qualified;
  }
  return map;
}

export function buildSqlCompletions(schema?: SchemaInfo, databases: string[] = [], engine = "", loadedSchemas: Record<string, SchemaInfo> = {}): SqlCompletions {
  const foreignKeys: Record<string, ForeignKey[]> = {};
  const tableColumns: Record<string, string[]> = {};
  const tableNames: Record<string, string> = {};
  const schemaTables: Record<string, string[]> = {};
  const schemaNames: Record<string, string> = {};
  const unqualified = new Map<string, { name: string; columns: string[] } | null>();
  const routines: { name: string; kind: string }[] = [];
  for (const schemaNode of schema?.schemas ?? []) {
    const schemaKey = schemaNode.name.toLowerCase();
    schemaNames[schemaKey] = schemaNode.name;
    schemaTables[schemaKey] = [];
    for (const table of [...schemaNode.tables, ...(schemaNode.views ?? [])]) {
      const tableKey = table.name.toLowerCase();
      const qualifiedKey = `${schemaKey}.${tableKey}`;
      const columns = table.columns.map((column) => column.name);
      tableColumns[qualifiedKey] = columns;
      tableNames[qualifiedKey] = `${schemaNode.name}.${table.name}`;
      unqualified.set(tableKey, unqualified.has(tableKey) ? null : { name: table.name, columns });
      schemaTables[schemaKey].push(table.name);
      for (const column of table.columns) {
        if (!column.references) continue;
        // references is schema.table.column; a key is useful from both ends.
        const parts = column.references.split(".");
        if (parts.length < 3) continue;
        const target = `${parts.slice(0, -2).join(".")}.${parts[parts.length - 2]}`.toLowerCase();
        const targetColumn = parts[parts.length - 1];
        (foreignKeys[qualifiedKey] ??= []).push({ column: column.name, table: target, toColumn: targetColumn });
        (foreignKeys[target] ??= []).push({ column: targetColumn, table: qualifiedKey, toColumn: column.name });
      }
    }
    routines.push(...(schemaNode.routines ?? []));
  }
  for (const [key, table] of unqualified) {
    if (!table) continue;
    tableColumns[key] = table.columns;
    tableNames[key] = table.name;
  }
  const databaseNames: Record<string, string> = {};
  const databaseSchemaNames: Record<string, Record<string, string>> = {};
  const databaseSchemaTables: Record<string, Record<string, string[]>> = {};
  const databaseTables: Record<string, string[]> = {};
  const databaseTableColumns: Record<string, string[]> = {};
  if (CROSS_DATABASE.has(engine.toLowerCase())) {
    // MySQL calls a database a schema; it is listed once, as a schema.
    for (const name of databases) if (!schemaNames[name.toLowerCase()]) databaseNames[name.toLowerCase()] = name;
    for (const [database, info] of Object.entries(loadedSchemas)) {
      const databaseKey = database.toLowerCase();
      databaseSchemaNames[databaseKey] = {};
      databaseSchemaTables[databaseKey] = {};
      databaseTables[databaseKey] = [];
      for (const schemaNode of info.schemas ?? []) {
        const schemaKey = schemaNode.name.toLowerCase();
        databaseSchemaNames[databaseKey][schemaKey] = schemaNode.name;
        databaseSchemaTables[databaseKey][schemaKey] = [];
        for (const table of [...schemaNode.tables, ...(schemaNode.views ?? [])]) {
          databaseSchemaTables[databaseKey][schemaKey].push(table.name);
          databaseTables[databaseKey].push(table.name);
          databaseTableColumns[`${databaseKey}.${schemaKey}.${table.name.toLowerCase()}`] = table.columns.map((column) => column.name);
          databaseTableColumns[`${databaseKey}.${table.name.toLowerCase()}`] = table.columns.map((column) => column.name);
        }
      }
    }
  }
  return { tableColumns, tableNames, schemaTables, schemaNames, routines, databaseNames, databaseSchemaNames, databaseSchemaTables, databaseTables, databaseTableColumns, foreignKeys, engine: engine.toLowerCase() };
}

/** Quote every object name completion with the connected engine's syntax. */
export function completionName(value: string, engine?: string): string {
  const normalized = engine?.toLowerCase() ?? "";
  if (normalized === "mysql" || normalized === "mariadb") {
    return value.split(".").map((part) => part.startsWith("`") && part.endsWith("`") ? part : `\`${part.replaceAll("`", "``")}\``).join(".");
  }
  if (normalized !== "sqlserver" && normalized !== "mssql" && !QUOTED_SQL_ENGINES.has(normalized)) return value;
  return value.split(".").map((part) => {
    if (normalized === "sqlserver" || normalized === "mssql") {
      if (part.startsWith("[") && part.endsWith("]")) return part;
      return `[${part.replaceAll("]", "]]")}]`;
    }
    if (part.startsWith('"') && part.endsWith('"')) return part;
    return `"${part.replaceAll('"', '""')}"`;
  }).join(".");
}

/** Where the cursor is, so the right kind of name is offered first. */
export type SqlClause = "select" | "from" | "where" | "group" | "order" | "set" | "other";

const CLAUSE_WORDS: Record<string, SqlClause> = {
  select: "select", from: "from", join: "from", into: "from", update: "from", table: "from",
  where: "where", on: "where", having: "where", and: "where", or: "where",
  group: "group", order: "order", set: "set", values: "set",
};

/**
 * Reads the clause the cursor sits in, ignoring anything inside brackets so a
 * subquery does not change the answer for the statement around it.
 */
export function clauseAt(sql: string, offset: number, engine?: string): SqlClause {
  let depth = 0;
  let clause: SqlClause = "other";
  for (const token of sqlTokens(sql.slice(0, offset), { hashComments: hashComments(engine) })) {
    if (token.kind === "symbol") {
      if (token.text === "(") depth++;
      else if (token.text === ")") depth = Math.max(0, depth - 1);
      else if (token.text === ";") { depth = 0; clause = "other"; }
      continue;
    }
    if (token.kind !== "word" || depth > 0) continue;
    const word = CLAUSE_WORDS[token.text.toLowerCase()];
    if (word) clause = word;
  }
  return clause;
}

/**
 * Offers the joins the foreign keys allow from the tables already named in
 * the statement, with the ON clause filled in.
 */
export function joinSuggestions(sql: string, completions: SqlCompletions): { label: string; insertText: string; detail: string }[] {
  const keys = completions.foreignKeys ?? {};
  const aliases = aliasMap(sql);
  // A statement may name a table with or without its schema; keys are held
  // under the qualified name.
  const qualify = (name: string) => (keys[name] ? name : Object.keys(keys).find((key) => key.endsWith("." + name)) ?? name);
  const used = new Set(Object.values(aliases).map(qualify));
  const aliasOf = (table: string) => Object.entries(aliases).find(([alias, target]) => qualify(target) === table && alias !== target && !alias.includes("."))?.[0];
  const out: { label: string; insertText: string; detail: string }[] = [];
  const seen = new Set<string>();
  for (const table of used) {
    for (const key of keys[table] ?? []) {
      if (used.has(key.table) || seen.has(`${table}:${key.table}:${key.column}`)) continue;
      seen.add(`${table}:${key.table}:${key.column}`);
      const name = completions.tableNames[key.table] ?? key.table;
      const here = aliasOf(table) ?? (completions.tableNames[table] ?? table);
      const short = shortAlias(name, used, out.length);
      out.push({
        label: `JOIN ${name}`,
        insertText: `JOIN ${completionName(name, completions.engine)} ${completionName(short, completions.engine)} ON ${completionName(`${short}.${key.toColumn}`, completions.engine)} = ${completionName(`${here}.${key.column}`, completions.engine)}`,
        detail: `foreign key ${key.column} → ${name}.${key.toColumn}`,
      });
    }
  }
  return out;
}

function shortAlias(name: string, used: Set<string>, index: number) {
  const base = (name.split(".").pop() ?? name).replace(/[^\w]/g, "").slice(0, 1).toLowerCase() || "t";
  return used.has(base) ? `${base}${index + 1}` : base;
}

/** A column offered on its own or through the table it belongs to. */
export interface ColumnSuggestion {
  label: string;
  insertText: string;
  detail: string;
  /** The bare name would be ambiguous, so only the qualified one is offered. */
  qualifiedOnly: boolean;
}

/**
 * Columns of the tables the statement already names. With more than one
 * table they are offered through their alias, and a name that exists in
 * several of them is offered only that way, since the bare name would not
 * resolve.
 */
export function columnSuggestions(sql: string, completions: SqlCompletions): ColumnSuggestion[] {
  const aliases = aliasMap(sql);
  const tables = new Map<string, string>();
  for (const [name, target] of Object.entries(aliases)) {
    const key = completions.tableColumns[target] || completions.databaseTableColumns?.[target] ? target : undefined;
    if (!key) continue;
    // The shortest name for the table: its alias when it has one.
    const current = tables.get(key);
    if (!current || name.length < current.length) tables.set(key, name === key ? completions.tableNames[key] ?? key : name);
  }
  const owners = new Map<string, string[]>();
  for (const [key] of tables) {
    for (const column of completions.tableColumns[key] ?? completions.databaseTableColumns?.[key] ?? []) {
      (owners.get(column) ?? owners.set(column, []).get(column)!).push(key);
    }
  }
  const out: ColumnSuggestion[] = [];
  for (const [column, keys] of owners) {
    const ambiguous = keys.length > 1;
    // With a single table there is nothing to tell apart, so the bare name is
    // the only one worth offering.
    for (const key of tables.size > 1 ? keys : []) {
      const owner = tables.get(key) ?? key;
      out.push({ label: `${owner}.${column}`, insertText: `${owner}.${column}`, detail: completions.tableNames[key] ?? key, qualifiedOnly: ambiguous });
    }
    if (!ambiguous) {
      const key = keys[0];
      out.push({ label: column, insertText: column, detail: completions.tableNames[key] ?? key, qualifiedOnly: false });
    }
  }
  return out;
}

/**
 * The columns a GROUP BY needs: everything selected that is not an
 * aggregate, with any alias dropped, so they can be inserted in one go.
 */
export function groupByColumns(sql: string, engine?: string): string[] {
  const tokens = sqlTokens(sql, { hashComments: hashComments(engine) });
  let start = -1;
  let end = tokens.length;
  let depth = 0;
  for (let index = 0; index < tokens.length; index++) {
    const token = tokens[index];
    if (token.kind === "symbol") {
      if (token.text === "(") depth++;
      else if (token.text === ")") depth = Math.max(0, depth - 1);
      continue;
    }
    if (token.kind !== "word" || depth > 0) continue;
    const word = token.text.toLowerCase();
    if (word === "select" && start < 0) start = index + 1;
    else if (start >= 0 && (word === "from" || word === "into")) { end = index; break; }
  }
  if (start < 0) return [];
  const parts: string[][] = [[]];
  depth = 0;
  for (const token of tokens.slice(start, end)) {
    if (token.kind === "symbol") {
      if (token.text === "(") depth++;
      else if (token.text === ")") depth = Math.max(0, depth - 1);
      else if (token.text === "," && depth === 0) { parts.push([]); continue; }
    }
    if (token.kind === "space" || token.kind === "comment") continue;
    parts[parts.length - 1].push(token.text);
  }
  const aggregates = /^(count|sum|avg|min|max|array_agg|string_agg|group_concat|listagg|stddev|variance|every|bool_and|bool_or)$/i;
  const out: string[] = [];
  for (const part of parts) {
    if (part.length === 0) continue;
    if (part.some((piece, index) => aggregates.test(piece) && part[index + 1] === "(")) continue;
    // Drop a trailing alias, written with or without AS.
    let pieces = part;
    const last = pieces[pieces.length - 1]?.toLowerCase();
    if (pieces.length >= 2 && pieces[pieces.length - 2].toLowerCase() === "as") pieces = pieces.slice(0, -2);
    else if (pieces.length >= 2 && /^[a-z_][\w$]*$/i.test(last ?? "") && !/^[.,]$/.test(pieces[pieces.length - 2])) pieces = pieces.slice(0, -1);
    const expression = pieces.join("");
    if (expression && expression !== "*" && !/^\d/.test(expression)) out.push(expression);
  }
  return out;
}

export function dotSuggestions(sql: string, identifier: string, completions: SqlCompletions): { kind: "column" | "table" | "schema"; values: string[]; owner?: string } {
  const ident = identifier.toLowerCase();
  const parts = ident.split(".");
  if (parts.length === 3) {
    const values = completions.databaseTableColumns?.[ident] ?? [];
    return { kind: "column", values, owner: identifier };
  }
  if (parts.length === 2) {
    if (DIRECT_DATABASE.has(completions.engine ?? "")) {
      const values = completions.databaseTableColumns?.[ident] ?? [];
      return { kind: "column", values, owner: identifier };
    }
    return { kind: "table", values: completions.databaseSchemaTables?.[parts[0]]?.[parts[1]] ?? [] };
  }
  const table = aliasMap(sql)[ident];
  const candidate = table?.toLowerCase();
  const tableKey = candidate && (completions.tableColumns[candidate] || completions.databaseTableColumns?.[candidate]) ? candidate : completions.tableColumns[ident] ? ident : undefined;
  if (tableKey) {
    return { kind: "column", values: completions.tableColumns[tableKey] ?? completions.databaseTableColumns?.[tableKey] ?? [], owner: completions.tableNames[tableKey] ?? tableKey };
  }
  if (completions.schemaTables[ident]) {
    return { kind: "table", values: completions.schemaTables[ident] };
  }
  // SQL Server: database.schema.table. Only the open database's schemas are known.
  if (completions.databaseNames?.[ident]) {
    if (DIRECT_DATABASE.has(completions.engine ?? "")) {
      return { kind: "table", values: completions.databaseTables?.[ident] ?? [] };
    }
    return { kind: "schema", values: Object.values(completions.databaseSchemaNames?.[ident] ?? completions.schemaNames) };
  }
  return { kind: "column", values: [] };
}
