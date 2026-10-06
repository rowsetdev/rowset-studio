/* global process, setTimeout, console, fetch */
import assert from "node:assert/strict";
import { spawn, execFileSync } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve, join } from "node:path";
import { chromium } from "playwright-core";

// A result on screen and nobody typing must cost no CPU. A render loop here
// does not look like a bug - nothing is wrong on screen - it just burns a
// core until the window is closed, so it needs a test that watches the cost
// rather than the picture.
const root = resolve(import.meta.dirname, "../..");
const binary = process.env.ROWSET_E2E_BIN || join(root, "dists/local/rowset");
const chrome = process.env.CHROME_BIN || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const directory = await mkdtemp(join(tmpdir(), "rowset-idle-e2e-"));
const database = join(directory, "idle.sqlite");
execFileSync("sqlite3", [database, "CREATE TABLE probe(id INTEGER PRIMARY KEY, label TEXT, qty INT); INSERT INTO probe(label,qty) VALUES('ada',7),('grace',NULL);"]);
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
  await request("/api/connections", "POST", { name: "Idle fixture", engine: "sqlite", host: "localhost", port: 1, database, environment: "dev", connectionUsername: "local", password: "local" }, `Bearer ${auth.accessToken}`);
  const ticket = await request("/api/local/open", "POST", undefined, `Bearer ${state.key}`);

  browser = await chromium.launch({ executablePath: chrome, headless: true, args: ["--no-sandbox"] });
  const context = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await context.newPage();
  await page.goto(`${base}/editor?desktop=idle-e2e#local=${ticket.ticket}`);
  await page.locator(".monaco-editor .view-lines").first().waitFor({ timeout: 30000 });
  await page.locator(".monaco-editor .view-lines").first().click();
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.type("SELECT id, label, qty FROM probe");
  await page.getByRole("button", { name: /^Run/ }).first().click();
  await page.getByText("Completed", { exact: false }).first().waitFor({ timeout: 30000 });
  await page.waitForTimeout(2500);

  const client = await context.newCDPSession(page);
  await client.send("Profiler.enable");
  await client.send("Profiler.setSamplingInterval", { interval: 1000 });
  await client.send("Profiler.start");
  const seconds = 5;
  await page.waitForTimeout(seconds * 1000);
  const { profile } = await client.send("Profiler.stop");

  const byId = new Map(profile.nodes.map(node => [node.id, node]));
  const deltas = profile.timeDeltas ?? [];
  let busy = 0;
  profile.samples.forEach((id, index) => {
    const name = byId.get(id)?.callFrame.functionName;
    if (name !== "(idle)" && name !== "(program)") busy += deltas[index] ?? 0;
  });
  const share = busy / (seconds * 1e6);
  console.log(`Idle E2E: ${(share * 100).toFixed(1)}% of ${seconds}s spent running JavaScript`);
  // A loop pegs a core: it reads as 90%+. Anything under a tenth is the
  // ordinary cost of a page that is simply sitting there.
  assert.ok(share < 0.10, `the editor burns ${(share * 100).toFixed(1)}% CPU with a result on screen and no input; something is re-rendering in a loop`);
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
