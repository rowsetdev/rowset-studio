// What each engine's shell looks like. Only the names differ between them -
// psql writes \dt where sqlite3 writes .tables and cqlsh writes DESCRIBE
// TABLES - so this file is the name table and consoleMeta.ts holds the one
// implementation each name points at.

/** How a non-meta line reaches the database. */
export type ConsoleKind = "sql" | "mongo" | "redis" | "cassandra" | "elasticsearch";

export interface ConsoleCommandSpec {
  /** What the user types, lower-cased for matching. */
  name: string;
  /** The key of the shared implementation in consoleMeta.ts. */
  action: string;
  /** Shown in help after the name, e.g. "[pattern]". */
  args?: string;
  summary: string;
  /** Passed to the action; lets one implementation serve several names. */
  option?: string;
}

export interface ConsoleEngineSpec {
  /** The word before the prompt's "=" or ">". */
  prompt: string;
  kind: ConsoleKind;
  /** The client this is modelled on, named in the console's first line. */
  client: string;
  /** Ends a statement. SQL engines buffer lines until one arrives. */
  terminator: ";" | "GO" | "none";
  commands: ConsoleCommandSpec[];
  /** One line under the banner saying what to type here. */
  hint: string;
}

const display: ConsoleCommandSpec[] = [
  { name: "\\x", action: "expanded", summary: "Switch between aligned columns and one line per field" },
  { name: "\\timing", action: "timing", summary: "Show how long each statement took" },
  { name: "\\clear", action: "clear", summary: "Clear the screen" },
  { name: "\\q", action: "quit", summary: "Close the console" },
];

// Nothing the user types travels to the database as session SQL: USE and its
// equivalents move the connection the next statement runs on instead, which
// is why every engine's "switch database" name lands on the same action.
function use(name: string, label: string): ConsoleCommandSpec {
  return { name, action: "use", args: `<${label}>`, summary: `Run the following statements against another ${label}` };
}

function helpCommand(name: string): ConsoleCommandSpec {
  return { name, action: "help", summary: "List the commands this engine's console understands" };
}

const postgres: ConsoleEngineSpec = {
  prompt: "postgres",
  kind: "sql",
  client: "psql",
  terminator: ";",
  hint: "Type SQL ending in ';', or a \\ command. \\? lists them.",
  commands: [
    helpCommand("\\?"),
    { name: "\\l", action: "databases", summary: "List databases" },
    use("\\c", "database"),
    { name: "\\dn", action: "schemas", summary: "List schemas" },
    { name: "\\dt", action: "tables", args: "[pattern]", summary: "List tables" },
    { name: "\\dv", action: "views", args: "[pattern]", summary: "List views" },
    { name: "\\di", action: "indexes", args: "[pattern]", summary: "List indexes" },
    { name: "\\ds", action: "sequences", args: "[pattern]", summary: "List sequences" },
    { name: "\\df", action: "routines", args: "[pattern]", summary: "List functions and procedures" },
    { name: "\\d", action: "describe", args: "[name]", summary: "Describe a table, or list tables when given no name" },
    ...display,
  ],
};

const mysql: ConsoleEngineSpec = {
  prompt: "mysql",
  kind: "sql",
  client: "mysql",
  terminator: ";",
  hint: "Type SQL ending in ';' or '\\G'. SHOW and DESCRIBE run on the server; \\h lists the rest.",
  commands: [
    helpCommand("\\h"),
    helpCommand("help"),
    use("use", "database"),
    use("\\u", "database"),
    { name: "status", action: "status", summary: "Show the connection, server version and current database" },
    { name: "\\s", action: "status", summary: "Show the connection, server version and current database" },
    ...display,
  ],
};

const mssql: ConsoleEngineSpec = {
  prompt: "mssql",
  kind: "sql",
  client: "sqlcmd",
  terminator: "GO",
  hint: "Type T-SQL and GO on its own line to run it. :help lists the rest.",
  commands: [
    helpCommand(":help"),
    use("use", "database"),
    use(":connect", "database"),
    { name: ":exit", action: "quit", summary: "Close the console" },
    ...display,
  ],
};

const file: ConsoleEngineSpec = {
  prompt: "sqlite",
  kind: "sql",
  client: "sqlite3",
  terminator: ";",
  hint: "Type SQL ending in ';', or a '.' command. .help lists them.",
  commands: [
    helpCommand(".help"),
    { name: ".databases", action: "databases", summary: "List databases" },
    { name: ".tables", action: "tables", args: "[pattern]", summary: "List tables" },
    { name: ".schema", action: "describe", args: "[name]", summary: "Show a table's columns, or every table when given no name" },
    { name: ".indexes", action: "indexes", args: "[pattern]", summary: "List indexes" },
    { name: ".timer", action: "timing", args: "[on|off]", summary: "Show how long each statement took" },
    { name: ".quit", action: "quit", summary: "Close the console" },
    { name: ".exit", action: "quit", summary: "Close the console" },
    ...display,
  ],
};

const clickhouse: ConsoleEngineSpec = {
  prompt: "clickhouse",
  kind: "sql",
  client: "clickhouse-client",
  terminator: ";",
  hint: "Type SQL ending in ';'. SHOW runs on the server; \\? lists the console's own commands.",
  commands: [
    helpCommand("\\?"),
    { name: "\\l", action: "databases", summary: "List databases" },
    use("use", "database"),
    { name: "\\dt", action: "tables", args: "[pattern]", summary: "List tables" },
    { name: "\\d", action: "describe", args: "[name]", summary: "Describe a table" },
    ...display,
  ],
};

const mongodb: ConsoleEngineSpec = {
  prompt: "mongodb",
  kind: "mongo",
  client: "mongosh",
  terminator: "none",
  hint: "Type db.collection.find({…}), aggregate, updateOne or deleteOne. 'help' lists the rest.",
  commands: [
    helpCommand("help"),
    { name: "show dbs", action: "databases", summary: "List databases" },
    { name: "show databases", action: "databases", summary: "List databases" },
    { name: "show collections", action: "tables", args: "[pattern]", summary: "List collections" },
    { name: "show tables", action: "tables", args: "[pattern]", summary: "List collections" },
    use("use", "database"),
    { name: "cls", action: "clear", summary: "Clear the screen" },
    ...display,
  ],
};

const redis: ConsoleEngineSpec = {
  prompt: "redis",
  kind: "redis",
  client: "redis-cli",
  terminator: "none",
  hint: "Type KEYS, SCAN, GET, SET, DEL or TTL. 'help' lists what this console supports.",
  commands: [
    helpCommand("help"),
    { name: "select", action: "use", args: "<index>", summary: "Switch to another database index" },
    { name: "\\clear", action: "clear", summary: "Clear the screen" },
    { name: "clear", action: "clear", summary: "Clear the screen" },
    { name: "\\timing", action: "timing", summary: "Show how long each command took" },
    { name: "\\x", action: "expanded", summary: "Switch between aligned columns and one line per field" },
    { name: "quit", action: "quit", summary: "Close the console" },
    { name: "exit", action: "quit", summary: "Close the console" },
  ],
};

const cassandra: ConsoleEngineSpec = {
  prompt: "cqlsh",
  kind: "cassandra",
  client: "cqlsh",
  terminator: ";",
  hint: "Type CQL ending in ';'. DESCRIBE and USE are handled here; 'help' lists them.",
  commands: [
    helpCommand("help"),
    { name: "describe keyspaces", action: "databases", summary: "List keyspaces" },
    { name: "describe tables", action: "tables", args: "[pattern]", summary: "List tables in the current keyspace" },
    { name: "describe table", action: "describe", args: "<name>", summary: "Describe a table" },
    { name: "describe", action: "describe", args: "[name]", summary: "Describe a table, or list tables when given no name" },
    use("use", "keyspace"),
    ...display,
  ],
};

const elasticsearch: ConsoleEngineSpec = {
  prompt: "elasticsearch",
  kind: "elasticsearch",
  client: "the Kibana console",
  terminator: "none",
  hint: "Type a search body as JSON, e.g. {\"index\":\"orders\",\"query\":{\"match_all\":{}}}. 'help' lists the rest.",
  commands: [
    helpCommand("help"),
    { name: "show indices", action: "tables", args: "[pattern]", summary: "List indices" },
    { name: "show indexes", action: "tables", args: "[pattern]", summary: "List indices" },
    use("use", "index"),
    ...display,
  ],
};

// Engines whose shells are the same client with a different name share one
// spec; only the word in the prompt changes.
function named(spec: ConsoleEngineSpec, prompt: string, client = spec.client): ConsoleEngineSpec {
  return { ...spec, prompt, client };
}

export const consoleEngines: Record<string, ConsoleEngineSpec> = {
  postgres: postgres,
  postgresql: postgres,
  cockroachdb: named(postgres, "cockroach", "cockroach sql"),
  mysql: mysql,
  mariadb: named(mysql, "mariadb", "mariadb"),
  mssql: mssql,
  sqlserver: mssql,
  sqlite: file,
  duckdb: named(file, "duckdb", "duckdb"),
  clickhouse: clickhouse,
  mongodb: mongodb,
  redis: redis,
  valkey: named(redis, "valkey", "valkey-cli"),
  cassandra: cassandra,
  elasticsearch: elasticsearch,
};

export function engineSpec(engine: string): ConsoleEngineSpec | undefined {
  return consoleEngines[engine];
}

// Longest name first, so "describe tables" is not matched as "describe", and
// "show databases" is not matched as "show dbs"'s shorter sibling.
export function sortedCommands(spec: ConsoleEngineSpec): ConsoleCommandSpec[] {
  return [...spec.commands].sort((left, right) => right.name.length - left.name.length);
}

/** The command a line starts with, and whatever follows it. */
export function matchCommand(spec: ConsoleEngineSpec, line: string) {
  const text = line.trim();
  const lower = text.toLowerCase();
  for (const command of sortedCommands(spec)) {
    const name = command.name.toLowerCase();
    if (lower !== name && !lower.startsWith(name + " ")) continue;
    // A backslash or dot command is its own token; a word like "use" must not
    // match "user_id" and is already guarded by the space check above.
    return { command, argument: text.slice(command.name.length).trim() };
  }
  return undefined;
}
