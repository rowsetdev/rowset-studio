import type { SchemaInfo, TableInfo } from "../editor/api.ts";
import type { ConsoleBlock } from "./consoleFormat.ts";
import type { ConsoleEngineSpec } from "./consoleEngines.ts";

// The one implementation behind every engine's meta commands. A name table
// in consoleEngines.ts decides which name reaches which function here, so
// \dt, .tables, "show collections" and "describe tables" share this code
// instead of each engine carrying its own.

export interface ConsoleSettings {
  expanded: boolean;
  timing: boolean;
}

export interface MetaContext {
  spec: ConsoleEngineSpec;
  engine: string;
  /** The database, keyspace or index the next statement runs against. */
  database: string;
  /** What the user typed after the command name, trimmed. */
  argument: string;
  /** Set by the command's spec; lets one action serve several names. */
  option?: string;
  settings: ConsoleSettings;
  /** The connection's schema. The caller caches it; this may be called often. */
  schema: () => Promise<SchemaInfo>;
  databases: () => Promise<string[]>;
  /** Side effects the page owns. */
  setDatabase: (name: string) => void;
  setSettings: (next: ConsoleSettings) => void;
  clear: () => void;
  quit: () => void;
  /** Named in the "status" output; the page knows them, this file does not. */
  connectionName: string;
  serverVersion?: string;
}

export type MetaAction = (context: MetaContext) => Promise<ConsoleBlock[]>;

/** A shell pattern - "ord*", "%ord%" or plain text - as a predicate. */
export function matcher(pattern: string): (name: string) => boolean {
  const trimmed = pattern.trim().replace(/^['"]|['"]$/g, "");
  if (!trimmed) return () => true;
  const expression = new RegExp(
    "^" + trimmed.replace(/[.+^${}()|[\]\\]/g, "\\$&").replace(/[*%]/g, ".*").replace(/[?_]/g, ".") + "$",
    "i",
  );
  // psql's \dt ord matches ord exactly, while \dt ord* matches a prefix.
  // Treating a pattern with no wildcard as "contains" is what people expect
  // from every other client, so match either way.
  return (name: string) => expression.test(name) || name.toLowerCase().includes(trimmed.toLowerCase());
}

function table(columns: string[], rows: unknown[][]): ConsoleBlock {
  return { kind: "result", columns, rows, rowCount: rows.length, durationMs: 0 };
}

function notice(text: string): ConsoleBlock[] {
  return [{ kind: "notice", text }];
}

/** Every object of one kind across all schemas, with its schema name. */
function objects(schema: SchemaInfo, pick: (node: SchemaInfo["schemas"][number]) => { name: string }[] | undefined) {
  const out: { schema: string; name: string }[] = [];
  for (const node of schema.schemas ?? []) {
    for (const item of pick(node) ?? []) out.push({ schema: node.name, name: item.name });
  }
  return out;
}

// Engines with no schema layer (SQLite, Redis, Elasticsearch, MongoDB) still
// report one node, named for the database. Printing a "Schema" column of the
// same repeated value is noise, so it only appears when it varies.
function objectRows(items: { schema: string; name: string }[], label: string): ConsoleBlock {
  const schemas = new Set(items.map(item => item.schema));
  if (schemas.size > 1) return table(["Schema", label], items.map(item => [item.schema, item.name]));
  return table([label], items.map(item => [item.name]));
}

function listing(label: string, pick: (node: SchemaInfo["schemas"][number]) => { name: string }[] | undefined): MetaAction {
  return async context => {
    const keep = matcher(context.argument);
    const items = objects(await context.schema(), pick).filter(item => keep(item.name));
    if (!items.length) return notice(context.argument ? `No ${label.toLowerCase()} matching "${context.argument}".` : `No ${label.toLowerCase()}.`);
    return [objectRows(items, label.replace(/e?s$/, ""))];
  };
}

// An index belongs to a table, so it is listed with the table's name beside
// it rather than on its own.
const indexes: MetaAction = async context => {
  const keep = matcher(context.argument);
  const rows: unknown[][] = [];
  for (const node of (await context.schema()).schemas ?? []) {
    for (const item of node.tables ?? []) {
      for (const index of item.indexes ?? []) {
        if (!keep(index.name) && !keep(item.name)) continue;
        rows.push([item.name, index.name, index.columns.join(", "), index.primary ? "primary" : index.unique ? "unique" : ""]);
      }
    }
  }
  if (!rows.length) return notice(context.argument ? `No indexes matching "${context.argument}".` : "No indexes.");
  return [table(["Table", "Index", "Columns", "Kind"], rows)];
};

function findTable(schema: SchemaInfo, name: string): { schema: string; table: TableInfo } | undefined {
  const [left, right] = name.includes(".") ? name.split(".", 2) : ["", name];
  for (const node of schema.schemas ?? []) {
    if (left && node.name.toLowerCase() !== left.toLowerCase()) continue;
    for (const item of [...(node.tables ?? []), ...(node.views ?? [])]) {
      if (item.name.toLowerCase() === right.toLowerCase()) return { schema: node.name, table: item };
    }
  }
  return undefined;
}

const describe: MetaAction = async context => {
  if (!context.argument) return listing("Tables", node => node.tables)(context);
  const schema = await context.schema();
  const found = findTable(schema, context.argument);
  if (!found) return [{ kind: "error", text: `No table or view named "${context.argument}".` }];
  const rows = found.table.columns.map(column => [
    column.name,
    column.dataType,
    column.nullable ? "" : "not null",
    column.pk ? "PK" : column.references ? `→ ${column.references}` : "",
    column.default ?? column.generated ?? "",
  ]);
  const blocks: ConsoleBlock[] = [
    { kind: "notice", text: `${found.schema ? `${found.schema}.` : ""}${found.table.name}` },
    table(["Column", "Type", "Null", "Key", "Default"], rows),
  ];
  const tableIndexes = found.table.indexes ?? [];
  if (tableIndexes.length) {
    blocks.push(table(["Index", "Columns", "Kind"], tableIndexes.map(index => [index.name, index.columns.join(", "), index.primary ? "primary" : index.unique ? "unique" : ""])));
  }
  return blocks;
};

const databases: MetaAction = async context => {
  const names = await context.databases();
  if (!names.length) return notice("This connection does not report a list of databases.");
  const keep = matcher(context.argument);
  const rows = names.filter(keep).map(name => [name, name === context.database ? "current" : ""]);
  return [table(["Name", ""], rows)];
};

const use: MetaAction = async context => {
  if (!context.argument) return [{ kind: "error", text: `${context.spec.commands.find(item => item.action === "use")?.name ?? "use"} needs a name.` }];
  const name = context.argument.replace(/;$/, "").replace(/^['"]|['"]$/g, "").trim();
  context.setDatabase(name);
  return notice(`Now running against ${name}.`);
};

// A toggle accepts psql's bare form and sqlite's ".timer on"/"off".
function toggle(key: keyof ConsoleSettings, label: string): MetaAction {
  return async context => {
    const word = context.argument.trim().toLowerCase();
    const next = word === "on" ? true : word === "off" ? false : !context.settings[key];
    context.setSettings({ ...context.settings, [key]: next });
    return notice(`${label} is ${next ? "on" : "off"}.`);
  };
}

const help: MetaAction = async context => {
  const seen = new Set<string>();
  const items: { name: string; summary: string }[] = [];
  // The spec lists the commands in the order they read best, with aliases of
  // one action shown once.
  for (const command of context.spec.commands) {
    const key = `${command.action}:${command.summary}`;
    if (seen.has(key)) continue;
    seen.add(key);
    items.push({ name: command.args ? `${command.name} ${command.args}` : command.name, summary: command.summary });
  }
  return [
    { kind: "notice", text: `${context.spec.client} commands Rowset's console understands:` },
    { kind: "commands", items },
    { kind: "notice", text: context.spec.hint },
  ];
};

const status: MetaAction = async context => {
  const rows: unknown[][] = [
    ["Connection", context.connectionName],
    ["Engine", context.engine],
    ["Database", context.database || "(none)"],
  ];
  if (context.serverVersion) rows.push(["Server", context.serverVersion]);
  rows.push(["Timing", context.settings.timing ? "on" : "off"], ["Expanded output", context.settings.expanded ? "on" : "off"]);
  return [table(["", ""], rows)];
};

const clear: MetaAction = async context => {
  context.clear();
  return [];
};

const quit: MetaAction = async context => {
  context.quit();
  return [];
};

export const metaActions: Record<string, MetaAction> = {
  help,
  clear,
  quit,
  status,
  use,
  databases,
  describe,
  indexes,
  schemas: async context => {
    const keep = matcher(context.argument);
    const names = ((await context.schema()).schemas ?? []).map(node => node.name).filter(keep);
    if (!names.length) return notice("No schemas.");
    return [table(["Schema"], names.map(name => [name]))];
  },
  tables: listing("Tables", node => node.tables),
  views: listing("Views", node => node.views),
  sequences: listing("Sequences", node => node.sequences),
  routines: async context => {
    const keep = matcher(context.argument);
    const rows: unknown[][] = [];
    for (const node of (await context.schema()).schemas ?? []) {
      for (const routine of node.routines ?? []) {
        if (keep(routine.name)) rows.push([node.name, routine.name, routine.kind]);
      }
    }
    if (!rows.length) return notice(context.argument ? `No routines matching "${context.argument}".` : "No routines.");
    return [table(["Schema", "Name", "Kind"], rows)];
  },
  timing: toggle("timing", "Timing"),
  expanded: toggle("expanded", "Expanded output"),
};
