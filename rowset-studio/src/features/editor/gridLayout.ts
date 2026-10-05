// How wide a result column should be, and how tall its rows are. A grid that
// gives every column the same generous width shows a third of the columns a
// database client shows on the same screen, so widths are measured from what
// the column actually holds and the extra space a comfortable row leaves is
// made optional.

export type GridDensity = "compact" | "comfortable";

export interface DensityMetrics {
  /** Row height in pixels, which the virtualiser also uses to place rows. */
  rowHeight: number;
  /** Tailwind classes for a cell's padding. */
  cellPadding: string;
  /** Whether the column's database type is shown beside its name. */
  showTypes: boolean;
}

export const densities: Record<GridDensity, DensityMetrics> = {
  compact: { rowHeight: 24, cellPadding: "px-2 py-0.5", showTypes: false },
  comfortable: { rowHeight: 32, cellPadding: "px-2 py-1.5", showTypes: true },
};

// Character widths at the grid's 12px font. The grid is not monospaced, so
// these are the averages that keep a column from cutting its own values off
// without measuring every string in the DOM.
const charWidth = 6.6;
const headerCharWidth = 6.9;
/** Border, padding and the room the sort control needs. */
const cellChrome = 18;
const headerChrome = 30;

/** A column never narrows past this, so a one-character name stays readable. */
export const minColumnWidth = 54;
/** Nor past this, so one long value cannot push every other column off-screen. */
export const maxColumnWidth = 320;

// Only the first rows decide the width. A result of 10,000 rows would other-
// wise measure 10,000 strings on every render, and the rows someone sees
// first are the ones the width has to suit.
const sampleRows = 120;

export function measureText(value: unknown): number {
  if (value == null) return 4; // "NULL"
  if (typeof value === "object") return JSON.stringify(value).length;
  return String(value).length;
}

/**
 * The width each column gets when the user has not resized it, measured from
 * the header and the first rows.
 */
export function autoColumnWidths(columns: string[], rows: unknown[][], types?: string[], density: GridDensity = "compact"): number[] {
  const limit = Math.min(rows.length, sampleRows);
  return columns.map((name, index) => {
    // The type is shown beside the name only when there is room for it.
    const headerText = densities[density].showTypes && types?.[index] ? `${name} ${types[index]}` : name;
    let longest = 0;
    for (let row = 0; row < limit; row++) {
      const length = measureText(rows[row]?.[index]);
      if (length > longest) longest = length;
      // Nothing is gained by measuring further once a column is already at
      // its widest.
      if (longest * charWidth + cellChrome >= maxColumnWidth) break;
    }
    const header = headerText.length * headerCharWidth + headerChrome;
    const content = longest * charWidth + cellChrome;
    return Math.round(Math.min(maxColumnWidth, Math.max(minColumnWidth, header, content)));
  });
}

/** True when a column holds XML, which is shown as a link to its own view. */
export function isXmlColumn(type: string | undefined, sample: unknown): boolean {
  if (type && type.toUpperCase().includes("XML")) return true;
  // A document, a processing instruction (SQL Server returns a plan or a
  // statement as "<?query ... ?>") or a declaration all open the same way.
  return typeof sample === "string" && /^\s*<[?!]?[A-Za-z_]/.test(sample) && sample.includes(">");
}

/**
 * Lays an XML document out one element per line, the way a database client's
 * XML viewer does. The text is never changed, only the whitespace between
 * tags, so what is shown is still what is stored.
 */
export function formatXml(xml: string): string {
  const text = xml.trim();
  if (!text) return text;
  // Split on tag boundaries, keeping the tags and the text between them.
  const parts = text.replace(/>\s+</g, "><").split(/(<[^>]*>)/).filter(part => part !== "");
  const lines: string[] = [];
  let depth = 0;
  for (const part of parts) {
    if (!part.startsWith("<")) {
      if (part.trim()) lines.push("  ".repeat(depth) + part.trim());
      continue;
    }
    const closing = part.startsWith("</");
    // <?xml ?>, <!-- -->, <!DOCTYPE> and <self-closing/> open nothing.
    const standalone = part.startsWith("<?") || part.startsWith("<!") || part.endsWith("/>");
    if (closing) depth = Math.max(0, depth - 1);
    lines.push("  ".repeat(depth) + part);
    if (!closing && !standalone) depth++;
  }
  return lines.join("\n");
}
