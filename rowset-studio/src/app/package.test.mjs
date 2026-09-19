import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

const read = (path) => JSON.parse(readFileSync(new URL(path, import.meta.url), "utf8"));

// The repository-root manifest publishes Studio as a package; it must name the
// same version and runtime dependencies as the app itself.
test("package manifest matches the Studio app", () => {
  const app = read("../../package.json");
  const library = read("../../../package.json");
  assert.equal(library.version, app.version);
  assert.deepEqual(library.dependencies, app.dependencies);
});
