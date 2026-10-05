// Turning a result into the text a shell prints. The console renders values
// as a database client does - aligned columns, NULL shown explicitly, one
// colour per value kind - so the work here is measuring and classifying,
// and the view only puts the pieces on screen.

/** What a value is, which decides how the view colours it. */
export type CellKind = "null" | "number" | "boolean" | "json" | "text";

export function cellKind(value: unknown): CellKind {
  if (value == null) return "null";
  if (typeof value === "number" || typeof value === "bigint") return "number";
  if (typeof value === "boolean") return "boolean";
  if (typeof value === "object") return "json";
  return "text";
}

/** The text a value prints as. NULL is the only value shown as a word. */
export function cellText(value: unknown): string {
  if (value == null) return "NULL";
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}

export interface Cell {
  text: string;
  kind: CellKind;
}

export interface Grid {
  columns: string[];
  /** Character width of each column, header and values included. */
  widths: number[];
  rows: Cell[][];
  /** Right-align numeric columns, as psql and the mysql client do. */
  numeric: boolean[];
}

// A row cap keeps the widest value from stretching a column past what fits;
// a single 2MB JSON document would otherwise set the width for the result.
const maxCellWidth = 120;

export function grid(columns: string[], rows: unknown[][]): Grid {
  const cells = rows.map(row => columns.map((_, index) => {
    const value = row[index];
    return { text: cellText(value), kind: cellKind(value) };
  }));
  const widths = columns.map((name, index) => {
    let width = displayWidth(name);
    for (const row of cells) {
      const cell = row[index];
      if (cell) width = Math.max(width, Math.min(displayWidth(cell.text), maxCellWidth));
    }
    return width;
  });
  // A column counts as numeric only when every value in it is a number;
  // one text value and the column reads better left-aligned.
  const numeric = columns.map((_, index) => {
    let seen = false;
    for (const row of cells) {
      const kind = row[index]?.kind;
      if (kind === "null") continue;
      if (kind !== "number") return false;
      seen = true;
    }
    return seen;
  });
  return { columns, widths, rows: cells, numeric };
}

// Wide characters (CJK, most emoji) take two terminal columns. Measuring
// them as one would leave every following column in the row misaligned.
export function displayWidth(text: string): number {
  let width = 0;
  for (const character of text) {
    const point = character.codePointAt(0) ?? 0;
    // Combining marks sit on the previous character and take no width.
    if (point >= 0x0300 && point <= 0x036f) continue;
    width += wide(point) ? 2 : 1;
  }
  return width;
}

function wide(point: number): boolean {
  return (
    (point >= 0x1100 && point <= 0x115f) ||
    (point >= 0x2e80 && point <= 0xa4cf) ||
    (point >= 0xac00 && point <= 0xd7a3) ||
    (point >= 0xf900 && point <= 0xfaff) ||
    (point >= 0xfe30 && point <= 0xfe6f) ||
    (point >= 0xff00 && point <= 0xff60) ||
    (point >= 0xffe0 && point <= 0xffe6) ||
    (point >= 0x1f300 && point <= 0x1faff) ||
    (point >= 0x20000 && point <= 0x3fffd)
  );
}

/** The text as it appears in a column, truncated with an ellipsis if wider. */
export function fitCell(text: string, width: number): string {
  if (displayWidth(text) <= width) return text;
  let out = "";
  let used = 0;
  for (const character of text) {
    const next = used + displayWidth(character);
    if (next > width - 1) break;
    out += character;
    used = next;
  }
  return out + "…";
}

export function padCell(text: string, width: number, right: boolean): string {
  const fitted = fitCell(text, width);
  const padding = " ".repeat(Math.max(0, width - displayWidth(fitted)));
  return right ? padding + fitted : fitted + padding;
}

/** The ├───┼───┤ rule under a header, and the ─ runs it is made of. */
export function rule(widths: number[], left: string, middle: string, right: string): string {
  return left + widths.map(width => "─".repeat(width + 2)).join(middle) + right;
}

export interface Record {
  index: number;
  fields: { name: string; cell: Cell }[];
}

// psql's \x: one block per row, one line per column. The only readable shape
// for a row with forty columns or a long JSON document in it.
export function records(columns: string[], rows: unknown[][]): Record[] {
  return rows.map((row, index) => ({
    index: index + 1,
    fields: columns.map((name, column) => ({ name, cell: { text: cellText(row[column]), kind: cellKind(row[column]) } })),
  }));
}

/** "12 rows" / "1 row", and the timing line when \timing is on. */
export function rowCountText(count: number): string {
  return count === 1 ? "1 row" : `${count} rows`;
}

export function durationText(milliseconds: number): string {
  if (milliseconds < 1000) return `${milliseconds} ms`;
  return `${(milliseconds / 1000).toFixed(3)} s`;
}

// Everything the console prints is one of these. Keeping the output as data
// rather than text means the view colours it and a test can assert on it.
export type ConsoleBlock =
  /** The prompt and the line the user typed, kept above its output. */
  | { kind: "echo"; prompt: string; text: string }
  | { kind: "result"; columns: string[]; rows: unknown[][]; rowCount: number; durationMs: number; truncated?: boolean; notice?: string }
  /** JSON documents, as the document engines return them. */
  | { kind: "documents"; documents: unknown[]; durationMs: number; truncated?: boolean; notice?: string }
  /** A statement that changed rows instead of returning them. */
  | { kind: "affected"; text: string; durationMs: number }
  | { kind: "notice"; text: string }
  | { kind: "error"; text: string; detail?: string }
  /** The command list, which is a table of its own shape. */
  | { kind: "commands"; items: { name: string; summary: string }[] };
