import type { ColumnInfo, SchemaNode, TableInfo } from "./api";
import { quoteIdentifier } from "./rowEdits.ts";

export interface SchemaDifference {
  id: string;
  table: string;
  object: string;
  kind: "table" | "column" | "index";
  status: "add" | "change" | "remove";
  source: string;
  target: string;
  sourceColumn?: ColumnInfo;
  targetColumn?: ColumnInfo;
}

function columnText(c: ColumnInfo) {
  return [c.dataType, c.nullable ? "NULL" : "NOT NULL", c.default ? `DEFAULT ${c.default}` : "", c.generated, c.pk ? "PRIMARY KEY" : "", c.references ? `REFERENCES ${c.references}` : "", c.comment ? `COMMENT ${c.comment}` : ""].filter(Boolean).join(" · ");
}

// Names are matched exactly: folding case would merge distinct quoted objects.
export function compareSchemas(source: SchemaNode, target: SchemaNode): SchemaDifference[] {
  const differences: SchemaDifference[] = [];
  const left = new Map(source.tables.map(t => [t.name, t]));
  const right = new Map(target.tables.map(t => [t.name, t]));
  for (const name of [...new Set([...left.keys(), ...right.keys()])].sort()) {
    const a = left.get(name), b = right.get(name);
    const add = (item: Omit<SchemaDifference, "id" | "table">) => differences.push({ ...item, table: name, id: JSON.stringify([name, item.kind, item.object]) });
    if (!a || !b) {
      add({ kind: "table", object: name, status: a ? "add" : "remove", source: a ? `${a.columns.length} columns` : "—", target: b ? `${b.columns.length} columns` : "—" });
      continue;
    }
    const ac = new Map(a.columns.map(c => [c.name, c])), bc = new Map(b.columns.map(c => [c.name, c]));
    for (const column of [...new Set([...ac.keys(), ...bc.keys()])].sort()) {
      const x = ac.get(column), y = bc.get(column);
      const sourceText = x ? columnText(x) : "—", targetText = y ? columnText(y) : "—";
      if (sourceText !== targetText) add({ kind: "column", object: column, status: !x ? "remove" : !y ? "add" : "change", source: sourceText, target: targetText, sourceColumn: x, targetColumn: y });
    }
    const indexes = (t: TableInfo) => new Map((t.indexes ?? []).map(i => [i.name, `${i.primary ? "PRIMARY" : i.unique ? "UNIQUE" : "INDEX"} (${i.columns.join(", ")})${i.includedColumns?.length ? ` INCLUDE (${i.includedColumns.join(", ")})` : ""}${i.filter ? ` WHERE ${i.filter}` : ""}`]));
    const ai = indexes(a), bi = indexes(b);
    for (const index of [...new Set([...ai.keys(), ...bi.keys()])].sort()) {
      if (ai.get(index) !== bi.get(index)) add({ kind: "index", object: index, status: !ai.has(index) ? "remove" : !bi.has(index) ? "add" : "change", source: ai.get(index) ?? "—", target: bi.get(index) ?? "—" });
    }
    if (a.columns.map(c => c.name).join("\0") !== b.columns.map(c => c.name).join("\0") && a.columns.length === b.columns.length && a.columns.every(c => bc.has(c.name))) {
      add({ kind: "table", object: name, status: "change", source: a.columns.map(c => c.name).join(", "), target: b.columns.map(c => c.name).join(", ") });
    }
  }
  return differences;
}

// Catalog summaries omit CHECK expressions, full FK definitions and index
// predicates. Never synthesize those, or copy defaults across schema names.
export function migrationDraft(engine: string, sourceSchema: string, targetSchema: string, changes: SchemaDifference[]) {
  const q = (name: string) => quoteIdentifier(engine, name);
  const comment = (text: string) => text.replace(/[\r\n\u2028\u2029]/g, " ");
  const lines = ["-- Migration draft: make the target match the source.", "-- Review dependencies and data before running. Manual steps are listed below."];
  for (const d of changes) {
    const table = (targetSchema && targetSchema !== "default" ? q(targetSchema) + "." : "") + q(d.table);
    const c = d.sourceColumn;
    lines.push("", `-- ${comment(d.status.toUpperCase() + " " + d.kind + " " + d.table + "." + d.object)}`);
    if (d.kind === "column" && d.status === "add" && c && !c.generated && !c.pk && !c.references && !c.comment && !c.default && c.nullable && ["postgres", "mysql", "mariadb", "mssql", "sqlserver", "sqlite", "duckdb"].includes(engine) && sourceSchema === targetSchema) {
      // Defaults may be literal values rather than SQL expressions on MySQL;
      // non-null additions require a backfill. Keep both as manual steps.
      lines.push(`ALTER TABLE ${table} ADD ${q(c.name)} ${c.dataType} NULL;`);
    } else {
      lines.push(`-- MANUAL: ${comment(d.target)} → ${comment(d.source)}`);
      if (d.status === "remove") lines.push("-- Removing this object can destroy data or break dependencies.");
      lines.push("-- Inspect source and target DDL and write the required statements.");
    }
  }
  return lines.join("\n") + "\n";
}
