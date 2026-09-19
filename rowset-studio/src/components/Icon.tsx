// Single uniform icon set. All icons share one visual language: 16px grid,
// 1.5 stroke, no fill, round caps/joins, currentColor. Add new glyphs here so
// the whole app stays one consistent style ("tek tip").
import type { ReactNode, SVGProps } from "react";

export type IconName =
  | "chevron-right"
  | "chevron-down"
  | "chevron-left"
  | "sql"
  | "activity"
  | "shield"
  | "lock"
  | "plug"
  | "users"
  | "database"
  | "table"
  | "columns"
  | "play"
  | "wand"
  | "format"
  | "save"
  | "explain"
  | "sun"
  | "moon"
  | "logout"
  | "power"
  | "close"
  | "plus"
  | "search"
  | "history"
  | "clock"
  | "upload"
  | "grid"
  | "text"
  | "filter"
  | "check"
  | "alert"
  | "panel-left"
  | "sort"
  | "key"
  | "bell"
  | "pencil"
  | "copy"
  | "refresh"
  | "more"
  | "notebook"
  | "bookmark"
  | "braces"
  | "download";

const paths: Record<IconName, ReactNode> = {
  "chevron-right": <path d="M6 4l4 4-4 4" />,
  key: (
    <>
      <circle cx="6" cy="6" r="2.75" />
      <path d="M8 8l5.5 5.5M11 11l1.5-1.5M13 13l1.5-1.5" />
    </>
  ),
  "chevron-down": <path d="M4 6l4 4 4-4" />,
  "chevron-left": <path d="M10 4L6 8l4 4" />,
  sql: (
    <>
      <rect x="2.5" y="3" width="11" height="10" rx="1.5" />
      <path d="M5 6.5h2.5M5 9.5h4M11 6v4" />
    </>
  ),
  activity: <path d="M2 8h3l1.5 4 3-8L13 8h1" />,
  shield: (
    <>
      <path d="M8 2l5 2v4c0 3-2.2 5-5 6-2.8-1-5-3-5-6V4l5-2z" />
      <path d="M6 8l1.5 1.5L10.5 6.5" />
    </>
  ),
  lock: (
    <>
      <rect x="3.5" y="7" width="9" height="6" rx="1.2" />
      <path d="M5.5 7V5a2.5 2.5 0 015 0v2" />
    </>
  ),
  plug: (
    <>
      <path d="M6 2v3M10 2v3" />
      <path d="M4.5 5h7v2.5a3.5 3.5 0 01-7 0V5z" />
      <path d="M8 11v3" />
    </>
  ),
  users: (
    <>
      <circle cx="6" cy="6" r="2.2" />
      <path d="M2.5 13c0-2 1.6-3.2 3.5-3.2s3.5 1.2 3.5 3.2" />
      <path d="M10.5 4.2A2 2 0 0113 6.2M11 9.9c1.6.1 2.9 1.2 2.9 3.1" />
    </>
  ),
  database: (
    <>
      <ellipse cx="8" cy="4" rx="5" ry="1.8" />
      <path d="M3 4v8c0 1 2.2 1.8 5 1.8s5-.8 5-1.8V4" />
      <path d="M3 8c0 1 2.2 1.8 5 1.8s5-.8 5-1.8" />
    </>
  ),
  table: (
    <>
      <rect x="2.5" y="3" width="11" height="10" rx="1" />
      <path d="M2.5 6.5h11M6.5 6.5V13" />
    </>
  ),
  columns: (
    <>
      <rect x="2.5" y="3" width="11" height="10" rx="1" />
      <path d="M6.5 3v10M9.5 3v10" />
    </>
  ),
  play: <path d="M5 3.5l7 4.5-7 4.5z" />,
  // Lines of different lengths: text being tidied up.
  format: (
    <>
      <path d="M3 4h10M3 8h6M3 12h8" />
      <path d="M11.5 11.5l1.5 1.5 2-2.5" />
    </>
  ),
  wand: (
    <>
      <path d="M4 12l7-7M10 3l.7 1.3L12 5l-1.3.7L10 7l-.7-1.3L8 5l1.3-.7z" />
      <path d="M4 3v2M3 4h2" />
    </>
  ),
  save: (
    <>
      <path d="M3 3h8l2 2v8H3z" />
      <path d="M5 3v3h5V3M5.5 13v-3h5v3" />
    </>
  ),
  explain: (
    <>
      <rect x="5.5" y="1.5" width="5" height="3" rx="0.6" />
      <rect x="1.5" y="11" width="5" height="3" rx="0.6" />
      <rect x="9.5" y="11" width="5" height="3" rx="0.6" />
      <path d="M8 4.5v3M8 7.5H4v3M8 7.5h4v3" />
    </>
  ),
  sun: (
    <>
      <circle cx="8" cy="8" r="2.8" />
      <path d="M8 1.5v1.5M8 13v1.5M1.5 8h1.5M13 8h1.5M3.4 3.4l1 1M11.6 11.6l1 1M12.6 3.4l-1 1M4.4 11.6l-1 1" />
    </>
  ),
  moon: <path d="M13 9.5A5.5 5.5 0 016.5 3a5.5 5.5 0 106.5 6.5z" />,
  logout: (
    <>
      <path d="M9.5 3H3.5v10h6" />
      <path d="M7 8h6M11 5.5L13.5 8 11 10.5" />
    </>
  ),
  power: (
    <>
      <path d="M8 2.5V8" />
      <path d="M4.8 4.5a4.8 4.8 0 106.4 0" />
    </>
  ),
  close: <path d="M4 4l8 8M12 4l-8 8" />,
  plus: <path d="M8 3v10M3 8h10" />,
  search: (
    <>
      <circle cx="7" cy="7" r="4" />
      <path d="M10 10l3 3" />
    </>
  ),
  download: (
    <>
      <path d="M8 2.5v8M4.5 7L8 10.5 11.5 7" />
      <path d="M3 13.5h10" />
    </>
  ),
  upload: (
    <>
      <path d="M8 10.5V3M5 5.8L8 3l3 2.8" />
      <path d="M3 10.5v2h10v-2" />
    </>
  ),
  clock: (
    <>
      <circle cx="8" cy="8" r="5.5" />
      <path d="M8 5v3.2l2 1.3" />
    </>
  ),
  history: (
    <>
      <path d="M3 8a5 5 0 105-5 5 5 0 00-4.5 2.8" />
      <path d="M3 3v2.8h2.8M8 5.5V8l2 1.2" />
    </>
  ),
  refresh: (
    <>
      <path d="M13 8a5 5 0 11-1.5-3.6" />
      <path d="M13 2.5v3h-3" />
    </>
  ),
  more: <path d="M3.5 8h.01M8 8h.01M12.5 8h.01" strokeWidth={2.5} />,
  notebook: (
    <>
      <rect x="3.5" y="2.5" width="9" height="11" rx="1.5" />
      <path d="M6 5.5h4M6 8h4M6 10.5h2.5" />
    </>
  ),
  bookmark: <path d="M4 2.5h8v11l-4-2.5-4 2.5v-11z" />,
  grid: (
    <>
      <rect x="2.5" y="2.5" width="11" height="11" rx="1" />
      <path d="M2.5 6h11M2.5 10h11M6 2.5v11" />
    </>
  ),
  text: <path d="M3 4h10M3 8h10M3 12h6" />,
  braces: <path d="M6 2.5c-1.5 0-2 .8-2 2v2c0 .8-.4 1.5-1.5 1.5.9 0 1.5.7 1.5 1.5v2c0 1.2.5 2 2 2M10 2.5c1.5 0 2 .8 2 2v2c0 .8.4 1.5 1.5 1.5-.9 0-1.5.7-1.5 1.5v2c0 1.2-.5 2-2 2" />,
  filter: <path d="M2.5 3.5h11l-4.2 5V13l-2.6-1.3V8.5z" />,
  check: <path d="M3 8.5l3 3 7-7" />,
  pencil: <path d="M11.5 2.5l2 2L5 13l-2.5.5L3 11z" />,
  copy: (
    <>
      <rect x="5.5" y="5.5" width="8" height="8" rx="1.5" />
      <path d="M10.5 5.5v-2a1 1 0 00-1-1h-6a1 1 0 00-1 1v6a1 1 0 001 1h2" />
    </>
  ),
  alert: (
    <>
      <path d="M8 2.5l6 10.5H2z" />
      <path d="M8 6.5v3M8 11.3v.2" />
    </>
  ),
  "panel-left": (
    <>
      <rect x="2.5" y="3" width="11" height="10" rx="1.2" />
      <path d="M6.5 3v10" />
    </>
  ),
  sort: <path d="M5 6l3-3 3 3M5 10l3 3 3-3" />,
  bell: (
    <>
      <path d="M4.5 7a3.5 3.5 0 017 0c0 2.5.8 3.6 1.3 4.2.3.3.1.8-.3.8H3.5c-.4 0-.6-.5-.3-.8C3.7 10.6 4.5 9.5 4.5 7z" />
      <path d="M6.8 13.5a1.4 1.4 0 002.4 0" />
    </>
  ),
};

export function Icon({
  name,
  size = 16,
  className = "",
  ...props
}: { name: IconName; size?: number } & SVGProps<SVGSVGElement>) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.5}
      strokeLinecap="round"
      strokeLinejoin="round"
      className={`shrink-0 ${className}`}
      aria-hidden="true"
      {...props}
    >
      {paths[name]}
    </svg>
  );
}
