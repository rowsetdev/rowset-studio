/* global process, setTimeout, console, fetch, document */
import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { createServer } from "node:net";
import { join, resolve } from "node:path";
import { chromium } from "playwright-core";

const root = resolve(import.meta.dirname, "../..");
const image = JSON.parse(await readFile(join(root, "scripts/audit/images.lock.json"), "utf8")).postgres;
const binary = process.env.ROWSET_E2E_BIN || join(root, "dists/local/rowset");
const chrome = process.env.CHROME_BIN || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const directory = await mkdtemp(join(tmpdir(), "rowset-postgres-browser-"));
const pgName = `rowset-browser-pg-${process.pid}`;
const password = "RowsetBrowser2026!Pass";
let desktop;
let browser;
function docker(...args) { return execFileSync("docker", args, { encoding: "utf8", timeout: 30000 }).trim(); }
async function waitFor(predicate, label, attempts = 120) {
  for (let i = 0; i < attempts; i++) {
    try { const value = await predicate(); if (value) return value; } catch { /* service still starting */ }
    await new Promise(resolve => setTimeout(resolve, 250));
  }
  throw new Error(`${label} did not become ready`);
}
async function stopDesktop() {
  if (!desktop || desktop.exitCode !== null) return;
  const exited = new Promise(resolve => desktop.once("exit", resolve));
  desktop.kill("SIGTERM");
  await Promise.race([exited, new Promise(resolve => setTimeout(resolve, 5000))]);
  if (desktop.exitCode === null) desktop.kill("SIGKILL");
}
async function freePort() {
  const server = createServer();
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  const port = server.address().port;
  await new Promise(resolve => server.close(resolve));
  return port;
}
try {
  const port = await freePort();
  docker("run", "-d", "--name", pgName, "--memory=512m", "-p", `127.0.0.1:${port}:5432`, "-e", `POSTGRES_PASSWORD=${password}`, "-e", "POSTGRES_DB=rowset_e2e", image);
  await waitFor(() => { docker("exec", pgName, "pg_isready", "-h", "127.0.0.1", "-U", "postgres", "-d", "rowset_e2e"); return true; }, "PostgreSQL");
  docker("exec", pgName, "psql", "-U", "postgres", "-d", "rowset_e2e", "-v", "ON_ERROR_STOP=1", "-c", "CREATE TABLE browser_probe(id integer PRIMARY KEY, label text NOT NULL); INSERT INTO browser_probe VALUES(1,'browser-ok');");
  desktop = spawn(binary, ["desktop"], { env: { ...process.env, ROWSET_DESKTOP_DIR: directory, ROWSET_DESKTOP_NO_BROWSER: "1", ROWSET_DETACHED: "1" }, stdio: "ignore" });
  const state = await waitFor(async () => JSON.parse(await readFile(join(directory, "instance.json"), "utf8")), "desktop");
  const base = `http://127.0.0.1:${state.port}`;
  async function request(path, method = "GET", body, authorization) {
    const response = await fetch(base + path, { method, headers: { ...(body ? { "content-type": "application/json" } : {}), ...(authorization ? { authorization } : {}) }, body: body ? JSON.stringify(body) : undefined });
    const data = await response.json();
    assert.ok(response.ok, `${method} ${path}: ${JSON.stringify(data)}`);
    return data;
  }
  const opened = await request("/api/local/open", "POST", undefined, `Bearer ${state.key}`);
  const auth = await request("/api/auth/local", "POST", { ticket: opened.ticket });
  const connection = await request("/api/connections", "POST", { name: "PostgreSQL browser fixture", engine: "postgres", host: "127.0.0.1", port, database: "rowset_e2e", environment: "dev", connectionUsername: "postgres", password, tlsMode: "disable" }, `Bearer ${auth.accessToken}`);
  assert.ok(connection.id);
  const ticket = await request("/api/local/open", "POST", undefined, `Bearer ${state.key}`);
  browser = await chromium.launch({ executablePath: chrome, headless: true, args: ["--no-sandbox"] });
  const page = await browser.newPage();
  await page.goto(`${base}/connections?desktop=browser-postgres#local=${ticket.ticket}`);
  await page.getByRole("heading", { name: "Connections" }).waitFor();
  await page.getByRole("row", { name: /PostgreSQL browser fixture/ }).getByRole("button", { name: "Open" }).click();
  await page.getByTitle("New query tab").waitFor();
  async function run(sql) {
    await page.locator(".monaco-editor .view-lines").first().click();
    await page.keyboard.press("ControlOrMeta+A");
    await page.keyboard.type(sql);
    await page.getByRole("button", { name: /^Run/ }).first().click();
  }
  await run("SELECT id, label FROM browser_probe;");
  try { await page.getByText("browser-ok").waitFor({ timeout: 10000 }); }
  catch (error) { console.log("query diagnostics:", (await page.locator("body").innerText()).slice(-1400)); throw error; }
  await page.getByRole("button", { name: "Edit rows" }).click();
  await page.getByTitle("Double-click to edit").filter({ hasText: "browser-ok" }).dblclick();
  await page.locator("td input").fill("browser-edited");
  await page.locator("td input").press("Enter");
  await page.getByRole("button", { name: "Review 1 change" }).click();
  await page.getByRole("button", { name: "Apply 1 statement" }).click();
  await page.getByText("browser-edited").waitFor();
  assert.equal(docker("exec", pgName, "psql", "-U", "postgres", "-d", "rowset_e2e", "-tAc", "SELECT label FROM browser_probe WHERE id=1"), "browser-edited");

  await run("SELECT pg_sleep(10);");
  await page.getByRole("button", { name: "Stop", exact: true }).click({ force: true });
  await page.getByRole("button", { name: "Stop", exact: true }).waitFor({ state: "hidden" });
  await run("SELECT label FROM browser_probe;");
  await page.getByText("browser-edited").waitFor();

  const tabCount = await page.getByTitle("Close tab").count();
  await page.getByTitle("New query tab").click();
  await page.waitForFunction(count => document.querySelectorAll('[title="Close tab"]').length === count + 1, tabCount);
  await run("SELECT pg_sleep(10);");
  await page.getByRole("button", { name: "Stop", exact: true }).waitFor();
  page.once("dialog", dialog => void dialog.accept());
  await page.getByTitle("Close tab").last().click();
  await page.waitForFunction(count => document.querySelectorAll('[title="Close tab"]').length === count, tabCount);

  docker("stop", pgName);
  await run("SELECT label FROM browser_probe;");
  await page.getByText("Failed", { exact: true }).waitFor();
  docker("start", pgName);
  await waitFor(() => { docker("exec", pgName, "pg_isready", "-h", "127.0.0.1", "-U", "postgres", "-d", "rowset_e2e"); return true; }, "restarted PostgreSQL");
  await run("SELECT label FROM browser_probe;");
  try { await page.getByText("browser-edited").waitFor({ timeout: 8000 }); }
  catch (error) { console.log("reconnect diagnostics:", (await page.locator("body").innerText()).slice(-1400)); throw error; }
  console.log("PostgreSQL browser E2E passed: grid edit, cancel/recover, database restart/reconnect");
} finally {
  await browser?.close();
  await stopDesktop();
  try { docker("rm", "-f", pgName); } catch { /* never started */ }
  await rm(directory, { recursive: true, force: true });
}
