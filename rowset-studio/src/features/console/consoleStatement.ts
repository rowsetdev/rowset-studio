// Deciding when a typed line is a whole statement. The rules are the client's,
// not the server's: psql waits for a semicolon, sqlcmd for a GO line of its
// own, and a shell with no terminator runs a line at a time unless a brace or
// a quote is still open. Nothing here reaches the network, so it is all
// directly testable.
import { matchCommand, type ConsoleEngineSpec } from "./consoleEngines.ts";
import { durationText, rowCountText } from "./consoleFormat.ts";

/** What the user has typed so far but not yet run. */
export interface Buffer {
  lines: string[];
}

export interface Pending {
  /** The statement to run, with its terminator removed. */
  statement: string;
  /** mysql's \G: print this one result expanded, whatever the setting is. */
  expandedOnce: boolean;
}

// Only quoting and bracket depth matter, and both have to be tracked across
// lines - a terminator inside a string or an unclosed brace does not end a
// statement.
interface Scan {
  depth: number;
  quote: string | null;
  /** True once a line has opened a comment that runs to its end. */
  lineComment: boolean;
  blockComment: boolean;
}

function scan(text: string, state: Scan): Scan {
  const next: Scan = { ...state, lineComment: false };
  for (let index = 0; index < text.length; index++) {
    const character = text[index];
    const pair = text.slice(index, index + 2);
    if (next.blockComment) {
      if (pair === "*/") {
        next.blockComment = false;
        index++;
      }
      continue;
    }
    if (next.lineComment) continue;
    if (next.quote) {
      if (character === "\\") index++;
      // '' and "" inside a quoted value are an escaped quote, not its end.
      else if (character === next.quote && text[index + 1] === next.quote) index++;
      else if (character === next.quote) next.quote = null;
      continue;
    }
    if (pair === "/*") {
      next.blockComment = true;
      index++;
      continue;
    }
    if (pair === "--" || character === "#") {
      next.lineComment = true;
      continue;
    }
    if (character === "'" || character === '"' || character === "`") {
      next.quote = character;
      continue;
    }
    if (character === "(" || character === "{" || character === "[") next.depth++;
    else if (character === ")" || character === "}" || character === "]") next.depth = Math.max(0, next.depth - 1);
  }
  return next;
}

/** The text of a buffer, minus anything a comment hides, for terminator checks. */
function lastCode(lines: string[]): { code: string; open: boolean } {
  let state: Scan = { depth: 0, quote: null, lineComment: false, blockComment: false };
  let code = "";
  for (const line of lines) {
    const before = state;
    state = scan(line, state);
    // Keep only what was outside a comment, so "select 1; -- note" ends and
    // "select 1 -- ;" does not.
    code += stripComments(line, before) + "\n";
  }
  return { code: code.trim(), open: state.depth > 0 || state.quote !== null || state.blockComment };
}

function stripComments(line: string, start: Scan): string {
  const state: Scan = { ...start, lineComment: false };
  let out = "";
  for (let index = 0; index < line.length; index++) {
    const character = line[index];
    const pair = line.slice(index, index + 2);
    if (state.blockComment) {
      if (pair === "*/") {
        state.blockComment = false;
        index++;
      }
      continue;
    }
    if (state.quote) {
      out += character;
      if (character === "\\") out += line[++index] ?? "";
      else if (character === state.quote && line[index + 1] === state.quote) out += line[++index];
      else if (character === state.quote) state.quote = null;
      continue;
    }
    if (pair === "/*") {
      state.blockComment = true;
      index++;
      continue;
    }
    if (pair === "--" || character === "#") break;
    if (character === "'" || character === '"' || character === "`") state.quote = character;
    out += character;
  }
  return out;
}

/**
 * Decides whether the buffer holds a whole statement yet. A meta command is
 * always whole: it is this console's own, never sent anywhere, so waiting for
 * a semicolon would only confuse.
 */
export function pending(spec: ConsoleEngineSpec, lines: string[]): Pending | undefined {
  const joined = lines.join("\n").trim();
  if (!joined) return undefined;
  if (lines.length === 1 && matchCommand(spec, lines[0])) return { statement: joined, expandedOnce: false };
  const { code, open } = lastCode(lines);
  if (!code) return undefined;
  if (spec.terminator === "GO") {
    // sqlcmd's GO is the batch separator; it is a line of its own and is not
    // part of the statement it runs.
    const last = lines[lines.length - 1].trim().toUpperCase();
    if (last !== "GO" || open) return undefined;
    const statement = lines.slice(0, -1).join("\n").trim();
    return statement ? { statement, expandedOnce: false } : undefined;
  }
  if (spec.terminator === ";") {
    if (open) return undefined;
    if (code.endsWith("\\G")) return { statement: code.slice(0, -2).trim(), expandedOnce: true };
    if (!code.endsWith(";")) return undefined;
    return { statement: code.replace(/;+$/, "").trim(), expandedOnce: false };
  }
  // Engines with no terminator run a line at a time, unless a brace, bracket
  // or quote is still open - a pasted multi-line document or pipeline.
  if (open) return undefined;
  return { statement: joined, expandedOnce: false };
}

// Kibana's "GET /orders/_search { … }" and a bare search body are both
// accepted; the index comes from the request, the path, or whichever one
// "use" selected, in that order.
export function elasticsearchBody(statement: string, index: string): string {
  const match = /^\s*(?:GET|POST)\s+\/?([^\s/]+)(?:\/_search)?\s*/i.exec(statement);
  const text = (match ? statement.slice(match[0].length) : statement).trim() || "{}";
  const parsed = JSON.parse(text) as Record<string, unknown>;
  const chosen = typeof parsed.index === "string" && parsed.index ? parsed.index : match?.[1] ?? index;
  return JSON.stringify({ ...parsed, index: chosen });
}

/** The "12 rows (3 ms)" line under a result, when timing is on. */
export function footer(rows: number | undefined, durationMs: number, timing: boolean): string {
  const count = rows === undefined ? "" : rowCountText(rows);
  if (!timing) return count;
  return count ? `${count} (${durationText(durationMs)})` : `(${durationText(durationMs)})`;
}

/**
 * The name in the prompt. SQLite and DuckDB name a database by its file path,
 * and a path is both too long to read and wide enough to squeeze the input
 * off the line, so only its file name is shown. The full path is still what
 * statements run against.
 */
export function promptLabel(spec: ConsoleEngineSpec, database: string): string {
  const name = database.trim();
  if (!name) return spec.prompt;
  const base = name.split(/[/\\]/).filter(Boolean).pop() ?? name;
  return base.length > 40 ? base.slice(0, 39) + "…" : base;
}
