# Rowset Studio

<table align="center"><tr>
  <td align="center" width="72"><img src="rowset-studio/src/assets/engines/postgres.png" height="36" alt="PostgreSQL" /></td>
  <td align="center" width="72"><img src="rowset-studio/src/assets/engines/mysql.png" height="36" alt="MySQL" /></td>
  <td align="center" width="72"><img src="rowset-studio/src/assets/engines/mariadb.png" height="36" alt="MariaDB" /></td>
  <td align="center" width="72"><img src="rowset-studio/src/assets/engines/mssql.svg" height="36" alt="SQL Server" /></td>
  <td align="center" width="72"><img src="rowset-studio/src/assets/engines/cockroachdb.svg" height="36" alt="CockroachDB" /></td>
  <td align="center" width="72"><img src="rowset-studio/src/assets/engines/clickhouse.svg" height="36" alt="ClickHouse" /></td>
  <td align="center" width="72"><img src="rowset-studio/src/assets/engines/mongodb.svg" height="36" alt="MongoDB" /></td>
  <td align="center" width="72"><img src="rowset-studio/src/assets/engines/redis.svg" height="36" alt="Redis" /></td>
  <td align="center" width="72"><img src="rowset-studio/src/assets/engines/cassandra.svg" height="36" alt="Cassandra" /></td>
  <td align="center" width="72"><img src="rowset-studio/src/assets/engines/elasticsearch.svg" height="36" alt="Elasticsearch" /></td>
</tr></table>

<p align="center">
  <b>A local-first SQL &amp; NoSQL workspace — one executable, no server to run, nothing to sign in to.</b>
</p>

<p align="center">
  <a href="https://github.com/rowsetdev/rowset-studio/releases"><img alt="Latest release" src="https://img.shields.io/github/v/release/rowsetdev/rowset-studio?display_name=tag&sort=semver&label=release"></a>
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/license-PolyForm%20Noncommercial%201.0.0-blue.svg"></a>
  <a href="rowset-core/go.mod"><img alt="Go" src="https://img.shields.io/github/go-mod/go-version/rowsetdev/rowset-studio?filename=rowset-core%2Fgo.mod"></a>
  <img alt="Platforms" src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey">
</p>

Rowset Studio runs entirely on your own computer. One executable starts a local
server on the loopback address, signs you in with a one-use ticket and opens
the Studio in your browser — no account, no cloud, no separate database to
install for the app itself. Credentials, notebooks and workspaces are
encrypted in a local SQLite database.

## Supported engines

| Engine | Connection | What's supported |
| --- | --- | --- |
| <img src="rowset-studio/src/assets/engines/postgres.png" height="18" valign="middle"> PostgreSQL | Host/port or URL, SSH tunnel | Full SQL editor, DDL, CSV import, row editing, schema compare |
| <img src="rowset-studio/src/assets/engines/mysql.png" height="18" valign="middle"> MySQL | Host/port or URL, SSH tunnel | Full SQL editor, DDL, CSV import, row editing, schema compare |
| <img src="rowset-studio/src/assets/engines/mariadb.png" height="18" valign="middle"> MariaDB | Host/port or URL, SSH tunnel | Full SQL editor, DDL, CSV import, row editing, schema compare |
| <img src="rowset-studio/src/assets/engines/mssql.svg" height="18" valign="middle"> SQL Server | Host/port or URL, SSH tunnel | SQL editor, DDL, CSV import, row editing, schema compare, estimated and actual plans |
| <img src="rowset-studio/src/assets/engines/cockroachdb.svg" height="18" valign="middle"> CockroachDB | Host/port (PostgreSQL wire protocol) | SQL editor, transactions, DDL, CSV import/export, row editing, estimated plans |
| SQLite | Absolute path to an existing file | SQL, transactions, schema, keys, indexes, DDL, CSV import/export, JSON export |
| DuckDB | Absolute path to an existing file; CGO build | SQL, transactions, tables/views, DDL, CSV import/export, JSON export |
| <img src="rowset-studio/src/assets/engines/clickhouse.svg" height="18" valign="middle"> ClickHouse | Native TCP: 9440 with TLS, 9000 without | SQL, databases, tables/views, DDL, CSV/JSON export, estimated plans |
| <img src="rowset-studio/src/assets/engines/mongodb.svg" height="18" valign="middle"> MongoDB | Host/port; `authSource=admin` | Compass-style filter/project/sort/skip/limit query bar synced with `db.collection.find()`, `db.collection.aggregate([...])` read-only pipelines, document insert/update/delete |
| <img src="rowset-studio/src/assets/engines/redis.svg" height="18" valign="middle"> Redis | Host/port | Pattern/type scan bar, keys grouped by type as pseudo-tables, string/hash key writes and delete |
| Valkey | Host/port | Redis-compatible pattern/type scan bar, key previews, string/hash key writes and delete |
| <img src="rowset-studio/src/assets/engines/cassandra.svg" height="18" valign="middle"> Cassandra | Contact points, keyspace, TLS/SSH, consistency/paging | Governed CQL reads/writes/DDL/batches, schema/DDL, CSV import, CSV/JSON export, CQL `INSERT JSON` export for base tables without counters |
| <img src="rowset-studio/src/assets/engines/elasticsearch.svg" height="18" valign="middle"> Elasticsearch | HTTP API host/port | Index/query/size search bar with `sort`/`search_after` pagination and `aggs`, index mapping browsing, document index/update/delete |

MongoDB, Redis, Valkey and Elasticsearch writes are limited to single-document/key
operations (no bulk APIs or transactions yet); MongoDB additionally has
read-only aggregation pipelines. Cassandra uses governed CQL. Each has a
query bar tailored to it — see
[Additional databases](#additional-databases-and-schema-comparison) below for
exact limits.

## Features

- **Connections** — paste a connection URL or fill in the form. TLS modes
  `disable`, `require`, `verify-ca` and `verify-full`, custom CA and client
  certificates. Several nodes per connection with primary/secondary detection
  and routing. Reach a database through an **SSH tunnel** (password or
  private key), with the server's host key trusted the first time and
  verified on every connection. A **Safe mode** switch per connection blocks
  every write statement, no separate read-only role required. **Test all**
  on the Connections page pings every filtered connection at once.
- **Command palette** (<kbd>⇧⌘K</kbd>) — jump to any connection or page
  without leaving the keyboard.
- **SQL editor** — run the statement at the cursor, the selection, or every
  statement in the tab (each keeps its own result). Auto-commit or manual
  commit mode with explicit Commit/Rollback, cancellation, formatting,
  open/download `.sql` files, and a **Snippets** menu to save and reuse
  statements per connection.
- **Execution plans** — Explain draws the plan of a statement as a diagram
  with cost heat and warnings; actual rows and timings on request for
  PostgreSQL, MySQL, MariaDB and SQL Server. CockroachDB and ClickHouse get a
  text plan (no actual-rows mode yet).
- **Edit rows** — change cells of a one-table result, review the generated
  UPDATE statements and apply them like any query.
- **CSV import** — import a CSV file into a table in one transaction, with a
  preview and column mapping.
- **Schema browser** — schemas, tables, views, routines, triggers, columns
  and indexes, with quick actions to open a table or copy names.
- **Notebooks** — Markdown notes and SQL cells together, encrypted and saved
  automatically. Export as Markdown or as a SQL script. **Save** in the
  editor adds the current query to a notebook; a cell opens in a new editor
  tab.
- **Row backups** — optionally save the rows an UPDATE or DELETE changes;
  Activity → Row backups opens a script that puts them back.
- **Schedules** — run a SELECT at set times in your time zone and save each
  result as a CSV or JSON file, while Rowset Studio is running.
- **Activity** — your statements across every connection, plus
  per-connection history in the editor.
- **Policies** — default guardrails (block `DELETE`/`UPDATE` without
  `WHERE`, `DROP`, `TRUNCATE`) plus an optional guardrail for table reads
  without `WHERE`, and your own
  rules: block a table, schema or statement type, limit rows, stop long
  queries, allow writes only in a time window.
- **Workspace autosave** — tabs survive restarts; concurrent edits from
  another window are detected instead of overwritten. Tabs can be exported
  and imported.

## Additional databases and schema comparison

**Schema comparison** is available in the sidebar and a database's menu.
Choose the desired source structure and the target database/schema. The page
compares table/column metadata and index summaries, and shows each table's
DDL side by side. A migration draft opens in the target SQL editor without
running. Only simple nullable column additions are generated; other changes
remain explicit manual steps. This is not a complete dependency-aware
migration tool: CHECK constraints, complete index/FK definitions, views and
routines need separate DDL review. Failed metadata reads are shown and
disable drafts. MongoDB, Redis, Valkey and Elasticsearch have no
table/column metadata to diff, so they don't appear in the connection
pickers.

MongoDB, Redis, Valkey and Elasticsearch support single-document/key
insert, update and delete from the query toolbar's **Write** action, behind
the same policy, read-only and audit rules as SQL writes. MongoDB also runs
read-only `aggregate()` pipelines, with `$out`, `$merge`, `$lookup` and
other writing/cross-collection/JavaScript stages rejected. Bulk APIs,
transactions and SSH tunnels are not yet supported for any of the four, and
index/primary-key/foreign-key metadata is not loaded for any
non-`database/sql` engine (SQLite, DuckDB, ClickHouse and these four).
Inline grid editing remains available only for the SQL engines, since it's
built on generated UPDATE/DELETE statements. Cassandra supports CSV import
in logged batches and CSV/JSON export. Query each engine's own tools
directly for operations Rowset doesn't cover yet.

Native `build-local-binary.sh` builds include DuckDB and require a working
C/C++ compiler. `CGO_ENABLED=0` builds, including the portable
cross-platform release script, omit DuckDB; the UI only offers it when the
server includes it. Cross-compiling with DuckDB requires a C/C++ toolchain
for the target platform.

## Query results and parameters

The result toolbar filters already loaded rows without querying the
database. Add conditions for a column or search all columns; conditions
combine with AND. Numeric comparisons preserve decimal and integer
precision. Grid, text and CSV/JSON exports use the filtered rows; edits
retain their original row identity.

SQL queries can use `{{name}}` value placeholders outside strings and
comments. The editor shows a parameter field for each name, with Text,
Number, Boolean and NULL types. Values live only in the current session.
Rowset expands typed SQL literals before sending a query through the normal
policies and execution path; this is not server-side prepared-statement
binding. Executed values appear in history. All script parameters are
checked before the first statement runs. Parameterized schedules and
multi-connection runs are not supported yet.

For MongoDB, select the connection in the same editor and open a collection
from the explorer. A Compass-style bar (Filter / Project / Sort / Skip /
Limit / Max Time MS) stays in sync with the raw
`{ "collection": "items", "filter": {}, "sort": {}, "limit": 100 }`
Extended JSON below it — editing either one updates the other. Run, Stop,
saved tabs, history and results use the common workspace. Formatting and
request construction preserve raw numeric literals; result documents retain
BSON type information.

## Install

**macOS and Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/rowsetdev/rowset-studio/main/install.sh | sh
```

**Windows** (PowerShell)

```powershell
irm https://raw.githubusercontent.com/rowsetdev/rowset-studio/main/install.ps1 | iex
```

The installer downloads the latest release, verifies its SHA-256 checksum and
installs for the current user only; no administrator rights are needed.

| System | Installed to |
| --- | --- |
| macOS | `~/Applications/Rowset Studio.app` (menu-bar app) and `~/.local/bin/rowset` |
| Linux | `~/.local/bin/rowset` and an applications-menu entry |
| Windows | `%LOCALAPPDATA%\Programs\Rowset Studio`, a Start menu entry and the user `PATH` |

Run the same command again to update. `ROWSET_VERSION=0.0.13` installs a
specific release. To uninstall, run `sh -s -- --uninstall` instead of `sh` on
macOS/Linux, or set `$env:ROWSET_UNINSTALL = 1` before the PowerShell
command; your connections and notebooks are kept.

Prefer to download yourself? Every
[release](https://github.com/rowsetdev/rowset-studio/releases) has archives
for macOS (Apple silicon, Intel and a universal app), Windows and Linux (x64
and ARM64) plus `SHA256SUMS`. Releases are not code-signed yet, so a file
downloaded with a browser triggers Gatekeeper or SmartScreen; on macOS,
`xattr -dr com.apple.quarantine "Rowset Studio.app"` clears it.

## Run

Open **Rowset Studio** from the applications menu, or run `rowset`. It starts
the local server, creates its private configuration on first launch, signs
you in with a one-use local ticket and opens your browser.

| Command | Effect |
| --- | --- |
| `rowset` or `rowset desktop` | start Rowset Studio, or open the running one |
| `rowset desktop-stop` | stop the running Rowset Studio |
| `rowset --version` | print the version |

**Shutdown Rowset** at the bottom of the sidebar stops the server. On macOS
the menu-bar icon also offers **Open Rowset** and **Quit**; quitting asks to
roll back open transactions.

Data lives in `Rowset/Community` under the user configuration directory
(`~/Library/Application Support` on macOS, `%AppData%` on Windows,
`~/.config` on Linux). Set `ROWSET_DESKTOP_DIR` to use another directory, for
example a disposable test workspace. The directory holds the encryption
keys; do not share it. Exported workspace JSON is **not** encrypted.

Rowset writes a copy of its database into `snapshots/` in the same directory
once a day and keeps the newest seven. Before a new version upgrades the
database it also writes a `before-…` copy, which is never rotated out. To go
back to a copy, quit Rowset and put the file in place of
`rowset-community.sqlite3`.

Statement history and the audit log are kept for as long as you keep them.
To remove old entries automatically, set a period in days:

| Variable | Removes |
| --- | --- |
| `ROWSET_QUERY_HISTORY_RETENTION_DAYS` | history entries older than this |
| `ROWSET_AUDIT_RETENTION_DAYS` | audit entries older than this |

A shared server applies 30 days of history and 90 days of audit unless these
are set; `0` keeps everything.

## Build from source

```sh
sh scripts/build-local-binary.sh                             # this computer
GOOS=windows GOARCH=amd64 sh scripts/build-local-binary.sh   # Windows, rowset.exe
sh scripts/package-macos.sh                                  # macOS menu-bar app
sh scripts/release-build.sh 0.0.13 release                   # every release asset
```

Pushing a `vX.Y.Z` tag that matches `rowset-studio/package.json` runs the
release workflow, which tests, builds and publishes the assets.

## Build requirements

- Go 1.25
- Node.js with npm (install Studio dependencies with `npm ci` in
  `rowset-studio`)
- For the macOS app: Xcode command-line tools (`swiftc`, `codesign`)

## Development

| Directory | Contents |
| --- | --- |
| `rowset-core` | Go server: API, database engines, local store |
| `rowset-parser` | SQL classification and rewriting used by policies |
| `rowset-studio` | React/TypeScript Studio (Vite, TanStack Query, Monaco) |
| `deploy/desktop` | macOS menu-bar wrapper |

```sh
(cd rowset-parser && go test ./...)
(cd rowset-core && go vet ./... && go test ./...)
(cd rowset-studio && npm run build && npm run lint && npm test)
GOCACHE=/tmp/rowset-build-cache sh scripts/build-local-binary.sh
(cd rowset-studio && npm run test:e2e) # local Chrome + sqlite3 required
(cd rowset-studio && npm run test:e2e:postgres) # local Chrome + Docker required
node --experimental-strip-types rowset-studio/scripts/bench-grid.mjs
```

Live engine tests run against real database servers and are skipped unless
their credentials are set:

The [14-engine client audit](reports/database-client-audit-2026-09-17.md) records
which operations were verified live and which remain unverified. To repeat its
container and file-engine probes one engine at a time, run
`python3 scripts/audit/run.py all` from the repository root. Image digests are
locked in `scripts/audit/images.lock.json`; JSON evidence is written under
`reports/audit-evidence/`.

| Engine | Variables |
| --- | --- |
| PostgreSQL / MySQL / MariaDB / SQL Server | `ROWSET_MATRIX_POSTGRES_PASSWORD`, `ROWSET_MATRIX_MYSQL_PASSWORD`, `ROWSET_MATRIX_MARIADB_PASSWORD`, `ROWSET_MATRIX_MSSQL_PASSWORD` |
| CockroachDB | `ROWSET_TEST_COCKROACHDB_HOST` (+ `_PORT`) |
| Redis | `ROWSET_TEST_REDIS_HOST` (+ `_PORT`) |
| Cassandra | `ROWSET_TEST_CASSANDRA_HOST` (+ `_PORT`) |
| Elasticsearch | `ROWSET_TEST_ELASTICSEARCH_HOST` (+ `_PORT`) |
| ClickHouse | `ROWSET_TEST_CLICKHOUSE_HOST`, optional `ROWSET_TEST_CLICKHOUSE_PASSWORD`, `ROWSET_MATRIX_CLICKHOUSE_PORT` |
| MongoDB | `ROWSET_TEST_MONGODB_HOST`, optional `ROWSET_MATRIX_MONGODB_PORT` |

CI runs isolated live-engine jobs for these engines alongside the self-contained
Go and Studio suites. The SQL live jobs check that each selected engine's plan
and transaction subtests actually pass, so a skipped integration test cannot
be mistaken for coverage.

The version lives in `rowset-studio/package.json`; the build scripts stamp
it into Studio, the executable and the macOS app. See
[CHANGELOG.md](CHANGELOG.md).

## License

Rowset Studio is source-available under the
[PolyForm Noncommercial License 1.0.0](LICENSE): the source is open to read,
fork and modify, and free to use for any noncommercial purpose, but not for
commercial use. For a commercial license, get in touch.
