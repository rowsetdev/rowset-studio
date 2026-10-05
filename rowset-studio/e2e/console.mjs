/* global process, setTimeout, console, fetch */
import assert from "node:assert/strict";
import { spawn, execFileSync } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve, join } from "node:path";
import { chromium } from "playwright-core";

// Drives the Console page against a real server and a real database. SQLite
// needs no container, so this runs anywhere `npm run test:e2e` does; the same
// script covers the other engines by pointing it at one of their connections.
const root = resolve(import.meta.dirname, "../..");
const binary = process.env.ROWSET_E2E_BIN || join(root, "dists/local/rowset");
const chrome = process.env.CHROME_BIN || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const directory = await mkdtemp(join(tmpdir(), "rowset-console-e2e-"));
const database = join(directory, "console.sqlite");
execFileSync("sqlite3", [database, "CREATE TABLE console_probe(id INTEGER PRIMARY KEY, label TEXT, qty INT); INSERT INTO console_probe(label,qty) VALUES('ada',7),('grace',NULL);"]);
const child = spawn(binary, ["desktop"], { env: { ...process.env, ROWSET_DESKTOP_DIR: directory, ROWSET_DESKTOP_NO_BROWSER: "1", ROWSET_DETACHED: "1" }, stdio: "ignore" });
let browser;
try {
  let state;
  for (let attempt = 0; attempt < 150; attempt++) {
    try { state = JSON.parse(await readFile(join(directory, "instance.json"), "utf8")); break; }
    catch { await new Promise(done => setTimeout(done, 100)); }
  }
  assert.ok(state?.port && state?.key, "desktop did not start");
  const base = `http://127.0.0.1:${state.port}`;
  async function request(path, method = "GET", body, authorization) {
    const response = await fetch(base + path, { method, headers: { ...(body ? { "content-type": "application/json" } : {}), ...(authorization ? { authorization } : {}) }, body: body ? JSON.stringify(body) : undefined });
    const value = await response.json();
    assert.ok(response.ok, `${method} ${path}: ${JSON.stringify(value)}`);
    return value;
  }
  const opened = await request("/api/local/open", "POST", undefined, `Bearer ${state.key}`);
  const auth = await request("/api/auth/local", "POST", { ticket: opened.ticket });
  await request("/api/connections", "POST", { name: "Console fixture", engine: "sqlite", host: "localhost", port: 1, database, environment: "dev", connectionUsername: "local", password: "local" }, `Bearer ${auth.accessToken}`);
  const ticket = await request("/api/local/open", "POST", undefined, `Bearer ${state.key}`);

  browser = await chromium.launch({ executablePath: chrome, headless: true, args: ["--no-sandbox"] });
  const page = await browser.newPage();
  await page.goto(`${base}/console?desktop=console-e2e#local=${ticket.ticket}`);
  const input = page.getByLabel("Console input");
  await input.waitFor();
  // A file database is named by its path; the prompt must still leave the
  // input room to be typed in.
  await input.click();
  assert.ok((await input.boundingBox())?.width > 100, "the prompt squeezed the input off the line");

  async function type(line, expected, label) {
    await input.click();
    await input.fill(line);
    await input.press("Enter");
    try { await page.getByText(expected, { exact: false }).last().waitFor({ timeout: 20000 }); }
    catch (error) {
      console.log(`console screen tail: ${(await page.locator("body").innerText()).slice(-800)}`);
      throw new Error(`${label ?? line}: expected ${JSON.stringify(expected)}`, { cause: error });
    }
  }

  await type("select label, qty from console_probe order by id;", "grace", "a SELECT prints its rows");
  await type("select label, qty from console_probe;", "NULL", "NULL is printed as a word");
  await type(".tables", "console_probe", ".tables lists tables");
  await type(".schema console_probe", "label", ".schema shows a table's columns");
  await type(".help", "List the commands", ".help lists the commands");
  await type(".timer on", "Timing is on", ".timer turns timing on");
  await type("select 1;", "ms", "timing prints a duration");
  await type("\\x", "Expanded output is on", "\\x turns expanded output on");
  await type("select label, qty from console_probe order by id;", "RECORD 1", "expanded output prints records");
  await type("\\x", "Expanded output is off", "\\x turns expanded output off");
  // A statement is held until its terminator arrives.
  await input.click();
  await input.fill("select label");
  await input.press("Enter");
  await type("from console_probe where label = 'ada';", "ada", "a statement spanning two lines runs on ';'");
  await type("select nosuchcolumn from console_probe;", "nosuchcolumn", "a failed statement prints the database's error");
  await type("update console_probe set qty = 8 where label = 'ada';", "Affected 1 row", "a write reports the rows it changed");
  await type(".tables nomatch", "No tables matching", "a pattern that matches nothing says so");
  await input.click();
  await input.press("ArrowUp");
  assert.ok((await input.inputValue()).length > 0, "ArrowUp recalled nothing");

  // The console is a client over the same API as the editor, so its
  // statements must appear in the audited history like any other.
  const session = await request("/api/local/open", "POST", undefined, `Bearer ${state.key}`);
  const again = await request("/api/auth/local", "POST", { ticket: session.ticket });
  const { history } = await request("/api/history", "GET", undefined, `Bearer ${again.accessToken}`);
  assert.ok((history ?? []).some(item => item.sql.includes("console_probe order by id")), "a console statement was not recorded in the history");
  assert.equal(execFileSync("sqlite3", [database, "SELECT qty FROM console_probe WHERE label='ada';"], { encoding: "utf8" }).trim(), "8", "the console's UPDATE did not reach the database");
  console.log("Console E2E passed: statements, meta commands, expanded output, timing, multi-line buffering, errors, writes and history");
} finally {
  await browser?.close();
  if (child.exitCode === null) {
    const exited = new Promise(done => child.once("exit", done));
    child.kill("SIGTERM");
    await Promise.race([exited, new Promise(done => setTimeout(done, 5000))]);
    if (child.exitCode === null) child.kill("SIGKILL");
  }
  await rm(directory, { recursive: true, force: true });
}
