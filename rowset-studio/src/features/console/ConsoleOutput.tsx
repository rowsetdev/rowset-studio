import { memo } from "react";
import { grid, padCell, records, rule, type Cell, type CellKind, type ConsoleBlock } from "./consoleFormat";
import { footer } from "./consoleStatement";

// Printing the blocks a run produced. A database shell gets its readability
// from alignment and from one colour per kind of value, so the layout is
// measured in consoleFormat and this file only puts it on screen.

const kindClass: Record<CellKind, string> = {
  null: "text-slate-400 italic dark:text-slate-500",
  number: "text-amber-700 dark:text-amber-300",
  boolean: "text-violet-700 dark:text-violet-300",
  json: "text-emerald-700 dark:text-emerald-300",
  text: "text-slate-700 dark:text-slate-200",
};

const line = "whitespace-pre leading-[1.45]";
const faint = "text-slate-400 dark:text-slate-600";

function Value({ cell, width, right }: { cell: Cell; width: number; right: boolean }) {
  return <span className={kindClass[cell.kind]}>{padCell(cell.text, width, right)}</span>;
}

function Table({ block, expanded }: { block: Extract<ConsoleBlock, { kind: "result" }>; expanded: boolean }) {
  if (expanded) {
    const blocks = records(block.columns, block.rows);
    const width = Math.max(0, ...block.columns.map(name => name.length));
    return (
      <div>
        {blocks.map(record => (
          <div key={record.index}>
            <div className={`${line} ${faint}`}>{`─[ RECORD ${record.index} ]${"─".repeat(Math.max(0, 24 - String(record.index).length))}`}</div>
            {record.fields.map(field => (
              <div key={field.name} className={line}>
                <span className="text-sky-700 dark:text-sky-300">{padCell(field.name, width, false)}</span>
                <span className={faint}> | </span>
                <span className={kindClass[field.cell.kind]}>{field.cell.text}</span>
              </div>
            ))}
          </div>
        ))}
      </div>
    );
  }
  const measured = grid(block.columns, block.rows);
  return (
    <div>
      <div className={line}>
        <span className={faint}>│ </span>
        {measured.columns.map((name, index) => (
          <span key={index}>
            <span className="font-semibold text-sky-700 dark:text-sky-300">{padCell(name, measured.widths[index], measured.numeric[index])}</span>
            <span className={faint}> │ </span>
          </span>
        ))}
      </div>
      <div className={`${line} ${faint}`}>{rule(measured.widths, "├", "┼", "┤")}</div>
      {measured.rows.map((row, rowIndex) => (
        <div key={rowIndex} className={line}>
          <span className={faint}>│ </span>
          {row.map((cell, index) => (
            <span key={index}>
              <Value cell={cell} width={measured.widths[index]} right={measured.numeric[index]} />
              <span className={faint}> │ </span>
            </span>
          ))}
        </div>
      ))}
    </div>
  );
}

// A document is shown as pretty-printed JSON rather than squeezed into a
// cell: it is the whole answer for MongoDB and Elasticsearch, not one value
// in a row.
function Documents({ block }: { block: Extract<ConsoleBlock, { kind: "documents" }> }) {
  return (
    <div>
      {block.documents.map((document, index) => (
        <div key={index} className={`${line} text-emerald-700 dark:text-emerald-300`}>
          {JSON.stringify(document, null, 2)}
        </div>
      ))}
    </div>
  );
}

function Commands({ items }: { items: { name: string; summary: string }[] }) {
  const width = Math.max(0, ...items.map(item => item.name.length));
  return (
    <div>
      {items.map(item => (
        <div key={item.name} className={line}>
          <span className="text-sky-700 dark:text-sky-300">{padCell(item.name, width, false)}</span>
          <span className={faint}>  </span>
          <span className="text-slate-600 dark:text-slate-300">{item.summary}</span>
        </div>
      ))}
    </div>
  );
}

function Block({ block, expanded, timing }: { block: ConsoleBlock; expanded: boolean; timing: boolean }) {
  switch (block.kind) {
    case "echo":
      return (
        <div className={line}>
          <span className="text-brand-500">{block.prompt}</span>
          <span className="text-slate-700 dark:text-slate-100">{block.text}</span>
        </div>
      );
    case "result": {
      const summary = footer(block.rowCount, block.durationMs, timing);
      return (
        <div>
          {block.rows.length > 0 && <Table block={block} expanded={expanded} />}
          {summary && <div className={`${line} ${faint}`}>{summary}</div>}
          {block.truncated && <div className={`${line} text-amber-700 dark:text-amber-400`}>Result capped; more rows remain.</div>}
          {block.notice && <div className={`${line} text-amber-700 dark:text-amber-400`}>{block.notice}</div>}
        </div>
      );
    }
    case "documents": {
      const summary = footer(block.documents.length, block.durationMs, timing);
      return (
        <div>
          <Documents block={block} />
          {summary && <div className={`${line} ${faint}`}>{summary}</div>}
          {block.truncated && <div className={`${line} text-amber-700 dark:text-amber-400`}>Result capped; more documents remain.</div>}
          {block.notice && <div className={`${line} text-amber-700 dark:text-amber-400`}>{block.notice}</div>}
        </div>
      );
    }
    case "affected": {
      const summary = footer(undefined, block.durationMs, timing);
      return (
        <div className={line}>
          <span className="text-slate-600 dark:text-slate-300">{block.text}</span>
          {summary && <span className={faint}> {summary}</span>}
        </div>
      );
    }
    case "notice":
      return <div className={`${line} text-slate-500 dark:text-slate-400`}>{block.text}</div>;
    case "error":
      return (
        <div>
          <div className={`${line} text-rose-700 dark:text-rose-300`}>{block.text}</div>
          {block.detail && <div className={`${line} text-rose-600/80 dark:text-rose-400/80`}>{block.detail}</div>}
        </div>
      );
    case "commands":
      return <Commands items={block.items} />;
  }
}

/** One run: the line the user typed and everything it printed. */
export interface ConsoleEntry {
  id: number;
  blocks: ConsoleBlock[];
  /** Fixed when the entry was printed, so a later toggle cannot reflow it. */
  expanded: boolean;
  timing: boolean;
}

const ConsoleOutput = memo(function ConsoleOutput({ entries }: { entries: ConsoleEntry[] }) {
  return (
    <div className="font-mono text-[12.5px]">
      {entries.map(entry => (
        <div key={entry.id} className="pb-2">
          {entry.blocks.map((block, index) => (
            <Block key={index} block={block} expanded={entry.expanded} timing={entry.timing} />
          ))}
        </div>
      ))}
    </div>
  );
});

export default ConsoleOutput;
