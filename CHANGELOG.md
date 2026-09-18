# Changelog

The version lives in `rowset-studio/package.json`. `scripts/build-local-binary.sh`
and `scripts/package-macos.sh` stamp it into Studio (sidebar), the `rowset`
executable and the macOS app. Every change set bumps the patch version and adds
an entry here.

## 0.0.98 — 2026-09-18

- Added: SSH tunnel support for MongoDB, Redis/Valkey and Elasticsearch,
  matching what the SQL engines and Cassandra already had. All three route
  every connection they open (Mongo's replica-set discovery included, ES's
  HTTP transport, Redis's single connection) through one SSH tunnel via a
  `DialContext`/`Dialer` adapter — the same pattern Cassandra already used.
  Removed the explicit "not supported yet" rejections and connection-form
  gates. Verified live: an in-process SSH test server forwarding real
  connections to Mongo/Redis/Elasticsearch containers, covering
  untrusted-host-key rejection, host-key learning, wrong-host-key
  rejection, and a working query through the tunnel — the same regression
  test the SQL engines already had, now table-driven to cover both.

## 0.0.97 — 2026-09-18

- Removed: `reports/database-client-audit-2026-09-17.md` and its
  `reports/audit-evidence/` — the report predated this session's
  Snowflake removal (still listed it as a live finding) and its
  MongoDB/Redis/Valkey/Elasticsearch write rows were marked unimplemented,
  which this session's row-backup work made false. `scripts/audit/run.py`
  still works to produce a fresh one.

## 0.0.96 — 2026-09-18

- Added: row backups for Redis/Valkey and Elasticsearch, completing row
  backups across every supported engine. Redis/Valkey capture a key's
  exact state with `DUMP`/`PTTL` before a write or delete and restore it
  byte-for-byte (with its original TTL) via `RESTORE`, or remove it if it
  didn't exist before — Redis has no distinct update mode, so a `Write`
  "Set" is backed up the same as a delete, unlike Mongo/Elasticsearch
  insert. Elasticsearch fetches the document's `_source` before an update
  or delete and restores it with a full re-index. Verified live: Redis Set
  on a new key, Set overwriting an existing key (with TTL), and Delete,
  each captured and restored correctly; Elasticsearch Insert (not backed
  up, matching Mongo), Update and Delete, each captured and restored
  correctly.

## 0.0.95 — 2026-09-18

- Added: row backups for Cassandra. A governed UPDATE/DELETE with a WHERE
  clause (USING TTL/TIMESTAMP and IF are tolerated) saves the matched rows
  first; Activity → Row backups restores them in one click, in CQL batches
  of up to 50, or opens a real, runnable BEGIN BATCH/APPLY BATCH script to
  review first. Supports the common scalar CQL types (text, int family,
  boolean, uuid/timeuuid, timestamp, blob, float/double, decimal, varint,
  inet); collections, tuples, UDTs, counters, duration, date and
  time-of-day columns are deliberately skipped rather than guessed at, with
  a reason shown in the run result, and counter tables are skipped
  entirely. Verified live: UPDATE and DELETE capture, both restore paths,
  and both skip cases (unsupported column type, counter table), round-trip
  a uuid/text/boolean/decimal/blob/timestamp row exactly.
- Docs: repo hygiene files added — CODE_OF_CONDUCT.md, CONTRIBUTING.md,
  issue templates and a pull request template.

## 0.0.94 — 2026-09-17

- Added: the MongoDB editor's Run button now executes `updateOne()`/
  `deleteOne()` typed directly into it, in shell syntax
  (`db.col.updateOne(filter, update)` / `db.col.deleteOne(filter)`) or the
  raw request JSON (`{"collection","filter","update"}`, or `{"collection",
  "filter","delete":true}`) — the same two forms `find()`/`aggregate()`
  already accepted. Previously only the Write dialog could run these;
  typing one into the editor and hitting Run failed with "Unsupported
  query field". Row backups, the WHERE guardrail and manual-commit
  transactions all apply exactly as they do from the Write dialog.
- Docs: updated README — CockroachDB gets actual (not just estimated) plans
  since it has `ExplainAnalyze`, noted MongoDB's row backups (the only NoSQL
  engine with them so far), and fixed the client-audit link's stale
  "14-engine" count after Snowflake's removal.

## 0.0.93 — 2026-09-17

- Fixed: reopening a MongoDB `find()` from Activity/History failed with
  "Max time must be between 1 and 600000 ms." The saved request always
  includes `maxTimeMs`, and 0 (the backend's own "no limit" default) was
  being rejected as out of range instead of accepted.
- Fixed: reopening an `aggregate()` from Activity/History failed outright
  ("Unsupported query field: pipeline"), because only its shell-syntax form
  was recognized — the raw `{collection,pipeline,...}` object History
  actually saves fell through to `find()`'s field validation. The query bar
  now also switches to Aggregate mode when reopening one.

## 0.0.92 — 2026-09-17

- Added MongoDB row backups: an update/delete from the Write dialog (or
  inside a manual transaction) can save the documents it is about to
  change first, the same "back up before UPDATE/DELETE" preference SQL
  writes already use. Activity → Row backups lists them with a Mongo shell
  script for review and a one-click Restore, which re-inserts (for a
  delete) or fully replaces by _id (for an update) — not a partial `$set`
  replay, since that can't be inverted field by field. Restore runs
  document by document rather than in one transaction, since MongoDB
  transactions need a replica set. Verified against a live server end to
  end, including clicking through Restore in the browser.

## 0.0.91 — 2026-09-17

- Added pagination to the Activity statements table: 20 rows per page with
  Previous/Next, resetting to page 1 whenever a filter changes.

## 0.0.90 — 2026-09-17

- Added MongoDB manual-commit transactions: the toolbar's manual-commit
  toggle begins a session/transaction, the Write dialog's insert/update/
  delete join it, and Commit/Rollback end it — same UI as SQL engines.
  Requires a replica set or mongos; a standalone server's rejection surfaces
  as the begin call's error.
- Fixed: MongoDB document insert returned an empty, unusable `_id` in its
  response (`bson.MarshalExtJSON` errors on a bare scalar value like an
  ObjectID; the error was silently swallowed). Found via manual browser
  testing of the transaction feature above, not a directed check — none of
  the existing tests asserted on the returned id's content, only that the
  call didn't error. Fixed and covered by a new regression check.
- ClickHouse's Go driver silently no-ops `Rollback()` — a transaction
  "succeeds" without reverting anything. Confirmed live and left disabled;
  MongoDB got the same check and passed for genuine (replica-set) support.

## 0.0.89 — 2026-09-17

- Added an Aggregate mode to the MongoDB query bar: a Find/Aggregate toggle
  and a pipeline editor, kept in sync with `db.collection.aggregate([...])`
  in the editor below it (previously only reachable by hand-typing shell
  syntax).
- Enabled CockroachDB's "Explain with actual rows" (the backend already
  supported EXPLAIN ANALYZE; only the capability flag hid it).
- Added a live CockroachDB transaction test (Begin/Execute/Rollback and
  Begin/Execute/Commit) and a live DuckDB one — both engines advertised
  Transactions but neither was exercised live before, unlike PostgreSQL,
  MySQL, MariaDB, SQL Server and SQLite.

## 0.0.88 — 2026-09-17

- Removed Snowflake support (connection type, driver, DSN/schema/DDL
  handling, engine picker and logo) to focus engine coverage elsewhere.
- Added Elasticsearch `sort`/`search_after` pagination past the 10,000-hit
  window and `aggs` in the search bar's raw JSON, with aggregation results
  and the next page's `search_after` values shown in the grid.
- Added MongoDB read-only aggregation: `db.<collection>.aggregate([...])` in
  the editor runs a pipeline, with `$out`, `$merge`, `$lookup` and other
  writing/cross-collection/JavaScript stages rejected before it runs.
- Added a "Test all" action on the Connections page to test every filtered
  connection at once.

## 0.0.87 — 2026-09-17

- Added MongoDB document insert/update/delete and Elasticsearch document
  index/update/delete, behind the same policy/read-only/audit guardrails as
  SQL writes; update requires operator documents ($set/$unset/…), never a
  full-document replace.
- Added Redis/Valkey string and hash-field writes and key deletion, with
  optional TTL.
- Added a "Write" action in the query toolbar for MongoDB, Redis/Valkey and
  Elasticsearch connections to drive these from Studio.
- Added CockroachDB and ClickHouse EXPLAIN (text plan, no ANALYZE yet).
- Added SQLite and DuckDB CSV import.
- Schema Compare's connection pickers now hide MongoDB, Redis, Valkey and
  Elasticsearch, which have no table/column metadata to diff.

## 0.0.86 — 2026-09-16

- Expanded Cassandra from personal read-only queries to policy-governed CQL
  reads, writes, DDL and batches in personal and shared workspaces, with audit
  logging, read-only enforcement and automatic schema cache invalidation.
- Added Cassandra contact points, consistency and page-size settings, plus TLS
  and SSH tunnel support.
- Added Cassandra table DDL viewing, CSV/JSON export and typed CSV import in
  bounded logged batches, with explicit partial-failure behavior.
- Added CQL-aware classification and automated/live integration coverage for
  comments, strings, multi-statements, batches, writes, DDL and imports.

## 0.0.85 — 2026-09-16

- Fixed sidebar navigation occasionally leaving the SQL editor visible after
  a query completed even though browser history had moved to the selected
  page. Rowset now detects that router/history desynchronization and recovers
  immediately without interfering with running-query or transaction prompts.
- Updated the Studio runtime and build chain to React 19.3, React Router 8.4,
  Vite 8.3 and the matching React Vite plugin and type definitions.

## 0.0.83 — 2026-09-15

- Server (Host/Port, or HA nodes) moved up next to username/password
  in the connection form, instead of near the bottom.
- Dropped the separate Alias field - it's derived from Name
  automatically now, same as it always was when left blank.
- Valkey has its real logo instead of a colored-initial badge.

## 0.0.82 — 2026-09-15

- HA nodes are opt-in now: a new connection shows a plain Host + Port
  row instead of always showing the multi-node "name / host / port /
  Remove" editor. A "This connection has more than one node" checkbox
  reveals the full editor when it's actually needed.

## 0.0.81 — 2026-09-15

- **Fixed Windows opening Internet Explorer instead of the default
  browser** for Rowset Studio, found on a real Windows Server: the
  old `rundll32 url.dll,FileProtocolHandler` trick goes through IE's
  own URL handler on some Windows builds. Switched to `cmd /c start`,
  which respects the actual default-browser association.
- Auto-refresh now allows `EXEC`/`EXECUTE`/`CALL` (a stored procedure
  call isn't a plain write), so watching a diagnostic proc like
  `sp_whoisactive` on an interval - the original motivating use case -
  actually works; plain DML/DDL keywords are still excluded.
- Query results show which physical node they ran against (as a small
  badge next to Completed), for connections with more than one node.
- **Added Valkey support**, wire-compatible with Redis so it reuses
  the same query editor and schema browsing.
- The New/Edit connection form: TLS now defaults to Off instead of
  full certificate verification; added "preprod" to Environment;
  username and password are next to each other instead of opposite
  ends of the form, and the form is visibly more compact overall.

## 0.0.80 — 2026-09-15

- **`rowset desktop` now survives closing the terminal it was started
  from**, on every platform. It used to run attached to whatever
  console launched it (typing `rowset` in cmd.exe/PowerShell and
  closing that window killed it, since Windows terminates a console
  process tree on close, and the same applies on Linux/macOS without
  something detaching it). It now re-execs itself once, detached
  (`Setsid` on macOS/Linux, `DETACHED_PROCESS` on Windows), and the
  original invocation waits for the detached copy to report itself
  ready before returning - so scripts calling `rowset desktop`
  synchronously still see a real failure if startup fails, but
  otherwise get their prompt back immediately.
- The macOS menu-bar app opts out of this (`ROWSET_DETACHED=1`) since
  it already manages the server process directly and needs to notice
  it crashing later, not just a failed launch - its behavior is
  unchanged.

## 0.0.79 — 2026-09-15

- **License changed from Apache 2.0 to the PolyForm Noncommercial
  License 1.0.0.** The source stays open to read, fork and modify,
  and free for any noncommercial use, but commercial use now needs a
  separate license.

## 0.0.78 — 2026-09-15

- Auto-refresh's active state is amber now (a better fit for "live"
  than the info-blue used elsewhere), and its pulsing dot no longer
  clips against the button's corner as it fades — it's a proper
  ping-ring indicator inline with the interval label instead.

## 0.0.77 — 2026-09-15

- Auto-refresh is now a single clock-icon button next to Run instead of
  a full-width "Auto-refresh: …" dropdown.
- Explain's icon is now a small node diagram instead of a generic
  circled-i.

## 0.0.76 — 2026-09-15

- **Auto-refresh**: a dropdown next to Run (Off/3s/5s/10s/30s) re-runs
  the current statement on an interval and updates the result in
  place — for watching a running process, a queue, or anything else
  worth checking every few seconds without hitting Run by hand. Only
  enabled for statements that look read-only; editing the statement
  into a write turns it back off.
- Fixed a real "index/PK/FK metadata not yet loaded" warning that was
  shown for every DuckDB and ClickHouse schema load regardless of
  whether that metadata was actually missing — it wasn't, for either
  of them. Both now load real primary keys, and DuckDB also loads
  real foreign keys and indexes; ClickHouse has no foreign keys at
  all, so there's nothing left to warn about there. Cassandra now
  also loads its secondary indexes and materialized views, which it
  previously flagged as simply not implemented.
- The sidebar and the connections list's "Open" link no longer trigger
  the browser's own link-target preview on hover — they navigate with
  the app's router instead of a real `<a href>`, since there's no
  actual new-tab use case for a one-use local session.

## 0.0.75 — 2026-09-15

- **Results filter redesign**: Filter and Edit rows are real bordered
  buttons now; Filter toggles a panel instead of adding a new
  condition on every click, with a dedicated "+ Add condition" button
  for additional ones; "Clear filters" reads as a (subdued) destructive
  action; and the row count moved out of the toolbar into the status
  bar, where it only lights up as "X of Y rows filtered" once a filter
  actually narrows something.
- The Primary/Secondary node picker no longer shows (disabled) for
  every single-node connection — only when the connection actually has
  more than one node to route between.

## 0.0.74 — 2026-09-15

- **Results Filter button is now visible**: it was plain text with no
  icon or hover state next to the properly-styled Edit rows button;
  now it matches, and highlights when a filter is active.
- The "X / Y loaded rows" counter only stands out (a highlighted "X of
  Y rows match" badge) once a filter actually narrows the result;
  otherwise it's a quiet "N rows loaded", so a filter matching every
  row (e.g. "any column contains 2" when a date column has a year like
  2026) doesn't read as broken.

## 0.0.73 — 2026-09-15

- **JSON view for query results**: a third view next to Grid and Text,
  syntax-colored, showing one JSON object per row. MongoDB and
  Elasticsearch results open in it by default, since their Grid view is
  a single "document" column of stringified JSON; every other engine
  still opens in Grid and can switch to JSON from the same toolbar.
- Fixed two lint errors from the previous change set (a useless
  assignment in the Elasticsearch query bar, and `eslint-disable`
  comments referencing a rule this project's config never registers).

## 0.0.72 — 2026-09-15

- **Query editors for Redis, Cassandra and Elasticsearch**: all three engines
  had a schema browser and a "not available yet" placeholder where the query
  editor should be; now they have real editors (a scan bar for Redis, CQL for
  Cassandra, a search bar for Elasticsearch), wired into `Run`, `Run all`,
  Monaco syntax highlighting and the "Open" row action in the schema tree,
  the same as every other engine.
- Fixed the Elasticsearch query bar sending an empty `{}` query clause (which
  Elasticsearch rejects as malformed) when the bar's fields hadn't been
  edited yet — it now defaults to `{"match_all":{}}`.

## 0.0.71 — 2026-09-15

- **Real logos** for CockroachDB, Snowflake, Redis, Cassandra and
  Elasticsearch (Simple Icons marks, recolored to each brand's color),
  replacing the colored-initial badges from the previous release.
- **ClickHouse no longer lists `INFORMATION_SCHEMA` and `information_schema`
  as two separate databases** — they're the same schema under a case-variant
  alias ClickHouse keeps for MySQL compatibility; only the canonical
  lowercase one is shown now.
- **Saved query snippets**: a "Snippets" menu in the SQL toolbar saves the
  current statement by name, per connection, and inserts a saved one back
  into the editor. The backend and API client already existed from an
  earlier change but had no UI until now.
- **Read-only connections**: a "Safe mode" checkbox on a connection blocks
  every write statement (INSERT/UPDATE/DELETE/DDL) on it, the same way a
  read-only role already does, but without needing to set one up. Shown as
  a "Safe mode" badge in the SQL toolbar and a lock badge in the connections
  list.

## 0.0.70 — 2026-09-15

- Redis, Cassandra and Elasticsearch pseudo-tables no longer show "Open
  SELECT", "Show DDL" or export actions in the schema tree — none of those
  paths work yet for these engines, and showing them just led to a confusing
  error. The auto-commit/transaction toggle is likewise hidden for them.
- CockroachDB, Snowflake, Redis, Cassandra and Elasticsearch get a colored
  initial badge instead of all sharing the same generic gray icon, so they
  are tellable apart in the connection list, sidebar and command palette.

## 0.0.69 — 2026-09-15

- **MongoDB query bar** now also populates itself from a query opened from
  Activity or History, not just typed shell syntax; those entries are saved
  as the raw find-request JSON, which the bar previously failed to parse and
  silently left at its defaults.

## 0.0.68 — 2026-09-15

- **Five new engines**: CockroachDB and Snowflake connect through the normal
  SQL editor (CockroachDB reuses PostgreSQL's wire protocol and catalogs
  directly; Snowflake uses its own `database/sql` driver — enter your account
  identifier as the server). Redis, Cassandra and Elasticsearch have working
  backends (key scan with type/TTL preview for Redis, read-only CQL SELECT
  for Cassandra, `_search` for Elasticsearch) reachable through the API, but
  their dedicated query screens are not built yet — for now their connections
  can be created, tested and schema-browsed from the sidebar; the query editor
  shows a notice instead of the SQL/Mongo editor. HA nodes and SSH tunnelling
  are not available for Redis, Cassandra, Elasticsearch or Snowflake yet.
- **Command palette** (⇧⌘K / Ctrl+Shift+K): jump to any page or saved
  connection from anywhere in Studio. Uses Shift so it doesn't collide with
  the schema explorer's existing ⌘K search-box shortcut.

## 0.0.67 — 2026-09-15

- **MongoDB query bar**: added Max Time MS to Options, mapped to the shell's
  `.maxTimeMS()` and enforced server-side as a per-query timeout.

## 0.0.66 — 2026-09-14

- **MongoDB query bar**, Compass-style, above the `db.collection.find()`
  editor: a Filter field plus an Options panel for Project, Sort, Skip and
  Limit. Editing the bar rewrites the editor's shell syntax; switching tabs
  re-reads the bar from the editor. `find()` now also accepts a projection as
  its second argument and `.skip()` in the chain. Collation and max time are
  left out of this pass; add them later if needed.

## 0.0.65 — 2026-09-14

- Fixed the MongoDB and ClickHouse logos rendering larger than the other
  engine logos in the schema tree; a global `height: auto` rule was
  overriding their `height` attribute for non-square source SVGs.

## 0.0.64 — 2026-09-14

- **MongoDB query editor** accepts shell syntax (`db.customers.find({}).sort({...}).limit(n)`)
  in addition to the JSON find form; removed the redundant explanation strip
  and SQL-only tools (Explain, "Explain with actual rows") from the Mongo
  editor toolbar.
- **Connection names must be unique** per org, checked case- and
  whitespace-insensitively on both create and update, with a clear error in
  the form. Existing duplicates are left as-is; connection pickers now show
  host/port/database next to a name that collides with another connection.
- **New Connection form**: Engine selector moved to the top of the form,
  above the engine-specific fields.

## 0.0.63 — 2026-09-13

- **Schema comparison** in the sidebar and a database's menu compares two
  selected schemas, showing added, changed and removed tables, columns and
  index summaries. Inspect the table DDL side by side. Migration drafts open
  in the existing SQL editor on the target connection without running them.
  Only simple nullable column additions are generated automatically; other
  changes are marked MANUAL. Incomplete metadata disables draft generation.
  CHECK constraints, full index/FK definitions, views and routines are outside
  this initial metadata comparison.
- **SQLite** connections open an existing local file, with SQL queries,
  transactions, table/view/trigger discovery, keys, indexes and original DDL.
- **DuckDB** connections open an existing local file, with SQL queries,
  transactions, table/view discovery and DDL. Native builds include the Go
  driver using CGO; portable non-CGO builds hide DuckDB in the new-connection
  form and reject it on the API. Index/key discovery is not yet available.
- **ClickHouse** connections use the native TCP protocol (9440 with TLS,
  9000 without), with database/table/view discovery, DDL and SQL queries.
  Interactive transactions and index/key discovery are not yet available.
- **Local result filters** combine column conditions without rerunning SQL;
  filtered CSV/JSON export and original row identities are preserved.
- **SQL value parameters** use `{{name}}` with text, number, boolean or NULL
  values; scripts validate all parameters before starting.
- **MongoDB in the Query editor** lists databases and collections, finds and sorts
  documents with Extended JSON filters, cancels reads and exports results as
  JSON. BSON types are preserved as canonical Extended JSON. SELECT, table,
  schema, timeout and row-limit policies apply to finds. This initial version
  is read-only, uses authSource=admin, and does not support aggregation,
  SRV URLs or SSH tunnels.
- Additional engines currently run in personal workspaces with one configured
  endpoint. SQL INSERT export, CSV import and inline row editing are not yet
  available for the new engines; SQL engines can export CSV/JSON. Application
  settings and query-result storage are unchanged.

## 0.0.62 — 2026-09-13

- `#` starts a comment only on MySQL and MariaDB. On PostgreSQL and SQL Server
  it is read as written, so the `#>` operator works and a `;` after it is seen
  as a second statement — which the multiple-statements guard then refuses,
  rather than the parser hiding it behind a comment. The statement classifier
  and the editor both split by the connection's engine.
- Results are no longer cut off at a hidden size. The 16 MB per-row and 32 MB
  per-result display limits, which stopped a large result mid-stream, are gone;
  the row-count policy is what bounds a result.
- Updated the build dependencies flagged by npm audit (browserslist, nanoid);
  they are development-only and not part of the app.
- Export a table as SQL: **Export as SQL (INSERT)** writes multi-row INSERT
  statements that quote the table and columns for the engine, write NULL for
  null and keep every value's type; a database can replay them. CSV exports now
  begin with a UTF-8 byte-order mark so Excel shows non-ASCII text (Turkish,
  say) correctly.
- The dialect-aware "#" handling now reaches every place statements are split:
  the editor's run and quick-fix paths, inline problem checks, autocomplete and
  Run on several connections. So a SQL Server "#temp" table and PostgreSQL "#>"
  are never mistaken for a comment, on any screen.

## 0.0.61 — 2026-09-13

- Fixed: Run on several connections and the SQL assistant were given the
  60-second limit ordinary API requests have. A script that ran longer was cut
  off, its statements cancelled, and progress reached the page only when the
  whole run had finished. Both now stream and run for as long as they need,
  like a single query.

## 0.0.60 — 2026-09-13

- A history read waits at most two seconds for statements still being
  recorded, then shows what is stored. Against the local database that wait is
  a few milliseconds; it keeps the history page from hanging when a remote
  activity store is slow or unreachable.

## 0.0.59 — 2026-09-13

- The database explorer keeps room at its right edge for the scrollbar macOS
  draws over the content while scrolling, so it no longer covers the column
  types aligned to that edge.

## 0.0.58 — 2026-09-13

- **Run on several connections** now runs on the server, up to ten
  connections at once (chosen in the dialog, four by default). A browser opens
  at most six connections to one address and shares them with the rest of
  Studio, so running from the page capped real concurrency at six and stalled
  everything else meanwhile. One request now streams each connection's
  progress; every statement still goes through the query handler as the same
  person, so policies, row backups, timeouts and history apply per connection,
  and results reach the page unchanged, large integers included. Stop all
  ends the statements on the databases themselves.
- A trigger on a table or view is listed under that table, after its columns
  and indexes, and the table shows that it has triggers. The Triggers group
  keeps only triggers without such a table.
- Definitions a database returns on one line — MySQL and MariaDB triggers and
  views, PostgreSQL triggers, SQL Server views and functions — are laid out on
  lines in Show DDL. Only whitespace between tokens changes; a definition the
  author wrote on lines is shown exactly as written. Copy and Open in editor
  take the text as shown.

## 0.0.57 — 2026-09-12

- The editor marks problems as you type, from the text and the schema already
  loaded, with no round trip to the database:
  - a string, quoted name or comment that is never closed, and a bracket that
    closes nothing (errors);
  - a table or view that is not in the loaded schema, and `alias.column` where
    the table has no such column, each with close names to change to;
  - a column two tables of the same query both have, used without saying
    which, with a fix to qualify it for each;
  - UPDATE or DELETE without WHERE.
- Checks stay quiet when a name cannot be verified: CTEs, tables the script
  creates, table functions, system catalogues, other databases, subquery
  aliases, and a table name that several schemas share where the search path
  decides — a column is missing only if no candidate has it. A subquery's
  columns are checked against its own tables, not the outer query's.
- `#` starts a comment only on MySQL and MariaDB when inspecting, so SQL Server
  temporary tables and PostgreSQL's `#>` no longer look like broken brackets.
- Fixed: the GROUP BY quick fix inserted at the wrong place in a statement that
  had blank lines before it.
- The quick-fix menu is wide enough to show the whole fix.

## 0.0.56 — 2026-09-12

- **Run on several connections** (editor ⋯ menu) runs the selection, or the
  whole editor, on the connections you pick, each in its own database. Up to
  four run at a time; on each one the statements run in order in auto-commit
  and stop at the first error without stopping the others. Every statement
  goes through the normal query endpoint, so policies, row backups and history
  apply to each connection as usual.
- A script that changes data or schema asks for confirmation first and names
  the production connections among the targets; changing the choice asks
  again. Anything not recognisably a read counts as a change — procedure
  calls, SET, EXPLAIN ANALYZE, SELECT … INTO, a CTE ending in DELETE.
- The Connections tab lists every connection with its status, statements,
  rows and time next to the result of the one picked, showing a failure first.
  Results whose columns match can be combined into one grid with the
  connection in the first column. Stop all cancels every running statement.

## 0.0.55 — 2026-09-12

- Statements are recorded by one background writer in batches instead of two
  writes on every request. With 48 concurrent writers the local store went from
  613 to about 46,000 records a second. A record is never dropped: when the
  queue is full a request waits for room rather than competing with the writer
  for SQLite's lock, and history still shows a statement as soon as it has run.
- The audit chain reads its newest entry through an index. At 200,000 audit
  entries that read took 40 ms on every statement, behind a lock; it now takes
  0.01 ms.
- History is listed through its index instead of sorting every statement a
  person ever ran (20 ms at 200,000 rows).
- Retention periods are applied once a day on a shared server. A personal
  workspace keeps its history and audit log unless a period is set explicitly.
  Old entries are removed in small batches, so statements keep being recorded
  meanwhile, and the cutoff is compared in the same format the entries use.
- Snapshots are taken at most once a day, after Rowset is already answering.
  Taking one on every start meant a few restarts in a day rotated out last
  week's copy, and a large database held up opening the app.
- Before a new version applies migrations to an existing database, Rowset
  copies it to `snapshots/before-<migration>-<time>.sqlite3` and does not
  upgrade when that copy cannot be written. The daily snapshot is taken after
  migrations, so it could never undo one that went wrong.
- A loaded schema is reused for 30 seconds and loaded once when the explorer,
  autocomplete, the diagram and the assistant ask at the same time. DDL, a
  script or a procedure run through Rowset, the end of a transaction, editing
  the connection and Refresh all read the catalog again straight away.

## 0.0.54 — 2026-09-12

- Rowset writes a snapshot of its own database into `snapshots/` beside it on
  every start and keeps the newest seven, so a bad migration or a mistake is
  recoverable. SQLite's VACUUM INTO takes the copy in one transaction, which
  keeps it consistent with the write-ahead log.
- Starting on a data directory with no database says so on the console, and
  names ROWSET_DESKTOP_DIR, instead of quietly coming up as a new installation
  with no connections in it.
- `scripts/restart-try.sh` rebuilds and restarts the local instance against its
  own data directory.

## 0.0.53 — 2026-09-12

- Columns of the tables in the statement are offered through their alias, and
  a column that several of those tables share is offered only that way, since
  the bare name would not resolve.
- A SELECT that aggregates without a GROUP BY offers one: the quick fix on the
  statement fills in the columns it has to group by.

## 0.0.52 — 2026-09-12

- Autocomplete follows the clause you are in: columns come first while
  selecting or filtering, tables after FROM and JOIN.
- After FROM, the joins your foreign keys allow are offered with the ON
  clause already written, using the alias already in the statement.

## 0.0.51 — 2026-09-12

- **Slack notifications**: paste an incoming webhook address in Account and
  Rowset posts when a scheduled query runs — every run or only failures —
  with how long it took, how many rows it wrote and the file it produced.
  A statement in the editor that takes longer than a threshold you set can
  report itself too. The rows are never sent, only what ran.
- Every group in the schema browser, tables included, starts collapsed;
  searching opens them so matches are never hidden.

## 0.0.50 — 2026-09-12

- The assistant moved from the bottom tabs to a panel beside the editor,
  opened with the Assistant button in the toolbar, so it can stay open while
  a statement runs and its results come in. Its state is remembered.
- Format has an icon of its own; the wand now means the assistant.

## 0.0.49 — 2026-09-12

- **AI assistant**, set up in Account: either your own Anthropic or OpenAI
  key, kept encrypted like a connection password, or the `claude` / `codex`
  command already signed in on this computer, in which case no key reaches
  Rowset at all.
- It can write a statement from the schema, explain the one in the editor,
  rewrite it to run faster using the indexes that exist, say which index
  would help, and explain a failed statement and correct it.
- Table and column names, their types and the indexes of the open database
  are sent as context, never table contents, and sharing the schema can be
  turned off.

## 0.0.48 — 2026-09-12

- SQL shown outside the editor is coloured by the editor's own tokenizer
  instead of a short keyword list, so every keyword, function, string and
  number reads exactly as it does while typing.

## 0.0.47 — 2026-09-12

- The Plan tab appears only once Explain has produced a plan, and goes away
  with the next run.

## 0.0.46 — 2026-09-12

- SQL shown outside the editor (an object's DDL, the statements to review
  before applying grid changes) uses the editor's own colours.

## 0.0.45 — 2026-09-12

- **Diagram**: a map of a database's tables and the foreign keys between
  them, with primary and foreign key columns marked. Open it from a
  database's ⋯ menu in the explorer; drag to move, scroll to zoom, filter by
  name, and click a table to pick out what it is related to.

## 0.0.44 — 2026-09-12

- **Add and delete rows in the result grid**, next to editing cells: Add row
  types a new row at the end, and clicking a row's number marks it for
  deletion. Review shows the INSERT, UPDATE and DELETE statements, coloured,
  before anything runs; they run through the editor, so policies, manual
  commit and row backups apply as to any statement.

## 0.0.43 — 2026-09-12

- The DDL of an object is shown with SQL colouring.

## 0.0.42 — 2026-09-12

- SQL Server table DDL writes PRIMARY KEY and UNIQUE instead of the
  catalog's PRIMARY_KEY_CONSTRAINT spelling.

## 0.0.41 — 2026-09-12

- **Show DDL** in the schema browser: the statement that creates a table,
  view, procedure, function, trigger or sequence. MySQL and MariaDB answer
  with their own SHOW CREATE text, SQL Server and PostgreSQL with their
  stored definitions, and CREATE TABLE is built from the catalog where the
  engine has no function for it (columns, defaults, identity, keys, checks,
  foreign keys and indexes).

## 0.0.40 — 2026-09-12

- Opening the SQL editor always collapses the navigation to icons, however
  it was left before. Expanding it there lasts for that visit; every other
  page keeps the width last chosen on such a page.

## 0.0.39 — 2026-09-12

- Fixed: the navigation kept its old width in the SQL editor. Every
  workspace already had an explicit expanded/collapsed setting saved, which
  overrode the new automatic mode; the setting now lives under its own name
  and starts as automatic.

## 0.0.38 — 2026-09-12

- The navigation shows only its icons in the SQL editor, where the explorer
  needs the width, and stays open on the other pages. Using the collapse
  button fixes your choice everywhere until you use it again.
- Shutdown Rowset is tinted red.

## 0.0.37 — 2026-09-12

- The row limit policy no longer describes itself as rewriting statements,
  because it does not: it reads the rows up to the limit and stops.
- Live tests run against a desktop instance put a policy's value back too,
  not only whether it was on, so a test run cannot leave a workspace with
  its own row limit.
- "Shutdown Rowset" stays on one line in the sidebar again.

## 0.0.36 — 2026-09-12

- The schema browser lists **sequences** (PostgreSQL, SQL Server, MariaDB)
  next to views, procedures, functions and triggers. All of these groups
  stay collapsed until you open them.
- Expanding a database reloads its schema, so objects created since the
  last look appear without pressing refresh.

## 0.0.35 — 2026-09-12

- The row limit no longer rewrites statements. Rowset runs the SQL exactly
  as written and stops reading once the cap is reached, then cancels the
  rest, so no statement can be made invalid by a LIMIT or TOP that the user
  did not write. Exports and scheduled files follow the same rule, and an
  export that stops at the cap says so.

## 0.0.34 — 2026-09-12

- **Results are capped at 10,000 rows by default**, by the "Limit result
  rows" policy, which existing workspaces also get. It rewrites the
  statement (LIMIT, or TOP on SQL Server), says so under the result and
  offers Export all rows. Change the number or turn it off in My policies.
- That rewriting now handles more statements: SELECT DISTINCT and CTEs on
  SQL Server, MySQL's `LIMIT offset, count`, `FETCH FIRST n ROWS ONLY`, and
  statements ending in FOR UPDATE or LOCK IN SHARE MODE, which used to
  produce invalid SQL or an error.

## 0.0.33 — 2026-09-12

Performance of large results, now that nothing caps them:

- Streaming a result no longer copies every row already received on each
  progress update, which made a long result slower the longer it ran.
  Progress is reported less often as a result grows.
- Row batches are also flushed by size, so a table with large values cannot
  produce one enormous line for the browser to parse.
- Saving the editor's tabs waits longer between saves for large workspaces
  instead of encrypting everything on each keystroke.

## 0.0.32 — 2026-09-12

- The editor no longer caps results at 1000 rows. A result is capped only
  by the "Limit result rows" policy, which then says so and offers
  **Export all rows (CSV)**. Rows stream in as they arrive and the grid
  only renders what is on screen; Stop still ends a run.

## 0.0.31 — 2026-09-12

- **Procedures, functions, triggers and events with BEGIN … END bodies**
  (MySQL, MariaDB, SQL Server) were rejected as "multiple SQL statements",
  and the editor split them at their semicolons. Both now keep the body in
  one statement; END IF / END LOOP and BEGIN TRANSACTION are understood.
- A live test now creates and uses tables with keys, indexes, views,
  functions, procedures (CALL / EXEC), triggers and sequences on every
  engine, and checks the schema browser lists them.
- No hidden limits without a policy: exports, imports, plans, schema reads
  and restores are no longer cut off after 60 seconds; the editor's row cap
  says "Showing the first 1000 rows" with **Show up to 10,000** and
  **Export all rows (CSV)** instead of claiming a policy; a query that hits
  the connection's query timeout says so and where to change it (default
  10 minutes, up to 24 hours, per connection); personal workspaces accept
  32 MB requests, 16 MB of saved tabs and 20 manual-commit transactions.
- Errors reading policies, scheduled runs or a table's column types are
  reported instead of silently showing defaults.
- A divider separates the theme switch from Shutdown Rowset.

## 0.0.30 — 2026-09-12

- Long queries: a personal workspace no longer stops statements after 8–10
  minutes (a connection's own query timeout and policy timeouts still
  apply), a manual transaction is never rolled back as idle while one of
  its statements is running, idle transactions are kept for an hour
  instead of 5 minutes, and the desktop app keeps the computer from idle
  sleep while statements, exports, imports or restores run.
- The personal owner is no longer rate limited, which could make a busy
  editor fail with "too many requests".
- NaN and Infinity values (PostgreSQL float and numeric) no longer break
  the result; they show and restore as `NaN`, `Infinity` and `-Infinity`.
- Restoring SQL Server `sql_variant` values keeps each value's own type.
- The all-types test now covers far more types (PostgreSQL ranges,
  geometric types, tsvector, arrays, special numbers; MySQL/MariaDB text
  and blob sizes, bit(64), decimal(65,30), spatial types, inet4/inet6/uuid;
  SQL Server text/ntext/image, max types, time(7), sql_variant,
  hierarchyid, geography, geometry) and runs over the HTTP API. With
  ROWSET_E2E_DESKTOP_DIR it runs against a running desktop instance, so
  its work shows in that instance's Activity and Row backups.

## 0.0.29 — 2026-09-11

Every data feature was run against a table of all common column types on
PostgreSQL, MySQL, MariaDB and SQL Server (full, edge-value and NULL rows),
plus a 20,000-row table. Fixed what that found:

- Row backups: restoring skips generated and computed columns (and SQL
  Server rowversion), leaves identity columns out of UPDATEs, and inserts
  deleted rows with `OVERRIDING SYSTEM VALUE` on PostgreSQL identity tables.
- **Restore** in Activity → Row backups now puts rows back in one
  transaction, with policies applied; on SQL Server identity tables it turns
  IDENTITY_INSERT on for it. The script stays available as **Script**.
- Results show dates as `2024-02-29`, times as `13:45:10.123` and
  timestamps without the ISO `T…Z` form, so exports import back into MySQL
  and generated SQL works on every engine.
- SQL Server uniqueidentifier values show as GUIDs instead of hex, and
  binary columns always show as hex.
- CSV import writes hex (`\x…`) into binary columns as bytes, and editing
  a binary cell in the grid generates a hex literal.

## 0.0.28 — 2026-09-11

- The browser's Back button asks before leaving the SQL editor, even when
  nothing is running; links in the app ask only when a query runs or a
  transaction is open.

## 0.0.27 — 2026-09-11

- Autocomplete offers database names on MySQL, MariaDB and SQL Server, and
  on SQL Server suggests schemas after `database.`.
- Leaving the SQL editor while a query runs or a transaction is open asks
  first; a trackpad side swipe no longer navigates back out of the app.
- The row backup setting explains how the 10,000-row check works.

## 0.0.26 — 2026-09-11

- More compact Database Explorer; counts in square boxes.
- Tables show Open SELECT and Copy name on hover again; their ⋯ menu has
  **Export as CSV / JSON** and Import CSV.
- Table export downloads the whole table. It runs as a SELECT with the same
  policies, row limits and result hooks as the editor, so the default
  "no SELECT without WHERE" policy blocks it until turned off.
- Scheduled queries now honour policy row limits too.
- The result grid's column resize handle is invisible until hovered.

## 0.0.25 — 2026-09-11

- Redesigned Database Explorer: a search box for databases, tables and
  columns (⌘K / Ctrl K), + to add a connection, engine groups separated by
  lines, rounded count pills, and a ⋯ menu on connections (refresh, edit)
  and tables (open SELECT, copy name, import CSV).
- A line separates Shutdown Rowset from the version in the sidebar.

## 0.0.24 — 2026-09-11

- Redesigned the unsaved-tabs screen: each copy lists its tabs with
  Restore tabs, Download and Discard. Restored or discarded copies no longer
  come back on the next start, copies whose tabs were saved after all are
  cleared silently, and tabs already in the workspace are not added twice.

## 0.0.23 — 2026-09-11

- The row backup setting moved to Account (on by default).
- When a statement's rows cannot be backed up (more than 10,000 rows, or an
  UPDATE on a table without a primary key), it does not run; Rowset asks
  whether to run it without a backup.
- Fixed: tabs opened from a row backup made the tab autosave fail with
  "invalid JSON request".

## 0.0.22 — 2026-09-11

- Running a restore script no longer makes a new row backup of its own.
- The row backup option moved from the toolbar to the ⋯ menu
  (“Back up rows before UPDATE/DELETE”).
- Row backups list the statement without its leading comments, and MySQL
  tables no longer show the database name twice.

## 0.0.21 — 2026-09-11

- Row backups are optional: the **Row backup** switch in the editor toolbar
  turns them on or off (on by default, remembered in the browser).

## 0.0.20 — 2026-09-11

- **Row backups**: before an UPDATE or DELETE on one table with a WHERE
  clause, Rowset saves the rows it is about to change (up to 10,000,
  encrypted, newest 100 kept). Activity → Row backups opens a restore script
  in a new editor tab: INSERTs for deleted rows, UPDATEs by primary key for
  changed ones. UPDATEs on tables without a primary key are not backed up,
  and the editor says why. A statement that fails keeps no backup.
- Fixed: `WHERE id <= 2` and `WHERE status != 'x'` were treated as always
  true, so the UPDATE/DELETE-without-WHERE policies blocked them.
- Redesigned CSV import dialog (drop zone, mapping table, progress) and a
  lighter schema tree: plain counts instead of boxed badges, compact rows,
  search with the refresh button inside.
- The light/dark switch sits next to Shutdown Rowset in the sidebar.

## 0.0.19 — 2026-09-11

- **Import CSV** from a table in the schema browser: preview, delimiter
  detection, header and empty-as-NULL options, column mapping. The file
  uploads in chunks and is inserted in one transaction, so either every row
  is imported or none is; policies apply as to any INSERT.
- Fixed: after switching tabs, Run and Explain could use the selection of
  the previous tab.
- A new run in a tab clears its old execution plan.
- The desktop sidebar no longer shows the account email and role, and
  Shutdown Rowset uses the normal text colour.

## 0.0.18 — 2026-09-11

- **Edit rows** in the result grid: when a result comes from one table and
  includes its primary key, double-click a cell to change it (or set NULL).
  Review shows the generated UPDATE statements; applying runs them through
  the editor (policies and manual commit apply) and reloads the result.
- Query results report which table column each result column comes from,
  also inside manual-commit transactions.

## 0.0.17 — 2026-09-11

- **Schedules** run a SELECT every day, on chosen weekdays or every few
  minutes, in a chosen time zone, and save each result as a new CSV or JSON
  file in a folder. They run while Rowset Studio is running, even with the
  browser closed; a run missed while it was closed either runs once when it
  opens or is skipped. Runs go through the same policies as the editor, are
  listed per schedule and appear in Activity. "Schedule this query" in the
  editor starts a schedule from the current statement. The SQL of a schedule
  is stored encrypted.

## 0.0.16 — 2026-09-11

- **Explain** draws the execution plan of the statement under the cursor in
  a new Plan tab: operators, row flow, cost heat, warnings and properties.
  PostgreSQL, MySQL, MariaDB and SQL Server show the estimated plan without
  running the statement; **Explain with actual rows** runs a SELECT to show
  actual rows and timings (PostgreSQL, MySQL, MariaDB). Policies apply to
  actual-row runs. The diagram comes from executionflow.

## 0.0.15 — 2026-09-11

- The desktop app has no password: it signs in through its launcher every
  time it opens. Sign out and the first-use password step are gone; an
  expired session shows how to open Rowset Studio again.
- Licensed under the Apache License 2.0; release archives include LICENSE.

## 0.0.14 — 2026-09-11

- First use asks the desktop owner to choose a password; signing in later
  needs only that password (no email).
- **Shutdown Rowset** sits at the bottom of the sidebar and confirms in the
  page instead of a browser dialog.
- Account page redesigned: profile, installation details and password change.

## 0.0.13 — 2026-09-11

- Releases: `scripts/release-build.sh` builds archives for macOS (arm64,
  amd64), Linux and Windows (amd64, arm64), a universal macOS app and
  `SHA256SUMS`; pushing a version tag publishes them through GitHub Actions.
- One-line installers for the current user: `install.sh` (macOS, Linux) and
  `install.ps1` (Windows), with checksum verification, update and uninstall.
- **Quit Rowset** in the sidebar stops the local server, rolling back open
  transactions after confirmation.
- The macOS app is named Rowset Studio.
- CI runs the Go and Studio tests on every push and pull request.

## 0.0.12 — 2026-09-11

First public snapshot of Rowset Studio.

- Local server with a one-use loopback sign-in; single executable for macOS,
  Windows and Linux, plus a macOS menu-bar app.
- Connections for PostgreSQL, MySQL, MariaDB and SQL Server with TLS modes
  (`disable`, `require`, `verify-ca`, `verify-full`), CA and client
  certificates, and multi-node topology with primary/secondary routing.
- SQL editor: run statement, selection or all statements with a result per
  statement, auto-commit or manual commit mode, cancellation, `.sql` open and
  download, workspace export and import.
- Transactions report whether they are active, aborted or lost; a lost
  transaction is cleaned up and reported instead of failing silently.
- Schema browser with table actions and compact search.
- Notebooks with Markdown notes and SQL cells, encrypted autosave, Markdown and
  SQL export; saved queries from earlier builds are imported once.
- Activity page with your statements across all connections.
- Default and custom policies enforced before a statement reaches the database.
- Workspace autosave with revision checks against concurrent windows.
