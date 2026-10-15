import { mongoQuery } from "./mongoQuery";
import { quoteIdentifier } from "./rowEdits.ts";
import { createContext } from "react";

export interface SchemaAction { sql: string; connectionId: string; database?: string; append?: boolean }
export const SchemaActions = createContext<(action: SchemaAction) => void>(() => undefined);

export function tableSelect(engine: string, schema: string, name: string, columns: string[] = []): string {
  if (engine === "mongodb") return mongoQuery(name);
  // Redis groups keys by type as a pseudo-table ("<db>.<type>"); open a scan
  // pre-filtered to that type.
  if (engine === "redis" || engine === "valkey") return `{"pattern":"*","type":${JSON.stringify(name)},"limit":100}`;
  // Elasticsearch's pseudo-tables are indices.
  if (engine === "elasticsearch") return `{"index":${JSON.stringify(name)},"query":{"match_all":{}},"size":100}`;
  const target = [schema, name].filter(Boolean).map(n => quoteIdentifier(engine, n)).join(".");
  const select = columns.length ? columns.map(n => quoteIdentifier(engine, n)).join(", ") : "*";
  return engine === "mssql" || engine === "sqlserver" ? `SELECT TOP (100) ${select}\nFROM ${target};` : `SELECT ${select}\nFROM ${target}\nLIMIT 100;`;
}
