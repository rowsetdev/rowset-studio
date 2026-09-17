/* global process, setTimeout, console, fetch, document */
import assert from "node:assert/strict";
import { spawn, execFileSync } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve, join } from "node:path";
import { chromium } from "playwright-core";

const root = resolve(import.meta.dirname, "../..");
const binary = process.env.ROWSET_E2E_BIN || join(root, "dists/local/rowset");
const chrome = process.env.CHROME_BIN || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const directory = await mkdtemp(join(tmpdir(), "rowset-browser-e2e-"));
const database = join(directory, "browser.sqlite");
execFileSync("sqlite3", [database, "CREATE TABLE probe(id INTEGER PRIMARY KEY, label TEXT); INSERT INTO probe VALUES(1,'browser-ok');"]);
const child = spawn(binary, ["desktop"], { env: { ...process.env, ROWSET_DESKTOP_DIR: directory, ROWSET_DESKTOP_NO_BROWSER: "1", ROWSET_DETACHED: "1" }, stdio: "ignore" });
let browser;
try {
  let state;
  for (let attempt = 0; attempt < 150; attempt++) {
    try { state = JSON.parse(await readFile(join(directory, "instance.json"), "utf8")); break; }
    catch { await new Promise(resolve => setTimeout(resolve, 100)); }
  }
  assert.ok(state?.port && state?.key, "desktop did not start");
  console.log("desktop ready");
  const base = `http://127.0.0.1:${state.port}`;
  async function request(path, method = "GET", body, authorization) {
    const response = await fetch(base + path, { method, headers: { ...(body ? { "content-type": "application/json" } : {}), ...(authorization ? { authorization } : {}) }, body: body ? JSON.stringify(body) : undefined });
    const value = await response.json();
    assert.ok(response.ok, `${method} ${path}: ${JSON.stringify(value)}`);
    return value;
  }
  const opened = await request("/api/local/open", "POST", undefined, `Bearer ${state.key}`);
  const auth = await request("/api/auth/local", "POST", { ticket: opened.ticket });
  const connection = await request("/api/connections", "POST", { name: "Browser fixture", engine: "sqlite", host: "localhost", port: 1, database, environment: "dev", connectionUsername: "local", password: "local" }, `Bearer ${auth.accessToken}`);
  console.log("fixture created");
  assert.ok(connection.id, "fixture connection was not created");
  const ticket = await request("/api/local/open", "POST", undefined, `Bearer ${state.key}`);
  browser = await chromium.launch({ executablePath: chrome, headless: true, args: ["--no-sandbox"] });
  const page = await browser.newPage();
  await page.goto(`${base}/connections?desktop=browser-e2e#local=${ticket.ticket}`);
  await page.getByRole("heading", { name: "Connections" }).waitFor();
  await page.getByRole("row", { name: /Browser fixture/ }).getByRole("button", { name: "Open" }).click();
  await page.getByTitle("New query tab").waitFor();
  async function setSQL(sql) {
    await page.locator(".monaco-editor .view-lines").first().click();
    await page.keyboard.press("ControlOrMeta+A");
    await page.keyboard.type(sql);
  }
  await setSQL("SELECT label FROM probe;");
  await page.getByRole("button", { name: /^Run/ }).first().click();
  try { await page.getByText("browser-ok").waitFor({ timeout: 10000 }); }
  catch (error) { console.log("result diagnostics:", (await page.locator("body").innerText()).slice(-1000)); throw error; }
  await page.getByRole("switch", { name: "Manual commit" }).click();
  await setSQL("INSERT INTO probe(label) VALUES('should-rollback');");
  await page.getByRole("button", { name: /^Run/ }).first().click();
  await page.getByRole("button", { name: "Rollback", exact: true }).click();
  await page.getByRole("button", { name: "Rollback", exact: true }).waitFor({ state: "hidden" });
  assert.equal(execFileSync("sqlite3", [database, "SELECT count(*) FROM probe WHERE label='should-rollback';"], { encoding: "utf8" }).trim(), "0", "rollback left an inserted row");
  await page.getByRole("switch", { name: "Manual commit" }).click();
  await setSQL("SELECT id, label FROM probe;");
  await page.getByRole("button", { name: /^Run/ }).first().click();
  await page.getByText("browser-ok").waitFor();
  assert.ok(await page.getByRole("button", { name: "Edit rows" }).isDisabled(), "SQLite lacks column-origin metadata, so grid editing must stay disabled");
  const tabsBefore = await page.getByTitle("Close tab").count();
  await page.getByTitle("New query tab").click();
  await page.waitForFunction(count => document.querySelectorAll('[title="Close tab"]').length === count + 1, tabsBefore);
  await page.getByTitle("Close tab").last().click();
  await page.waitForFunction(count => document.querySelectorAll('[title="Close tab"]').length === count, tabsBefore);
  assert.equal(await page.getByTitle("Close tab").count(), tabsBefore, "tab did not close");
  console.log("Browser E2E passed: desktop sign-in, connection, query, transaction rollback, tab open/close; SQLite grid editing is unavailable");
} finally {
  await browser?.close();
  if (child.exitCode === null) {
    const exited = new Promise(resolve => child.once("exit", resolve));
    child.kill("SIGTERM");
    await Promise.race([exited, new Promise(resolve => setTimeout(resolve, 5000))]);
    if (child.exitCode === null) child.kill("SIGKILL");
  }
  await rm(directory, { recursive: true, force: true });
}
