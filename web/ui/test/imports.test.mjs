
import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";

const ROOT = path.resolve(".");
const BARE = new Set(["preact", "preact/hooks", "htm"]);

function jsFiles(dir, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) { if (e.name !== "vendor") jsFiles(p, out); }
    else if (e.name.endsWith(".js")) out.push(p);
  }
  return out;
}

test("every import in js/ resolves (module graph is loadable)", () => {
  const files = jsFiles(path.join(ROOT, "js"));
  const errors = [];
  let checked = 0;
  for (const file of files) {
    const src = fs.readFileSync(file, "utf8");
    const re = /(?:from|import)\s+["']([^"']+)["']/g;
    let m;
    while ((m = re.exec(src))) {
      const spec = m[1];
      checked++;
      if (BARE.has(spec)) continue;
      if (!spec.startsWith(".") && !spec.startsWith("/")) {
        errors.push(`${path.relative(ROOT, file)}: bare specifier "${spec}" is not in the import map`);
        continue;
      }
      const target = spec.startsWith("/")
        ? path.join(ROOT, spec.replace(/^\//, ""))
        : path.resolve(path.dirname(file), spec);
      if (!fs.existsSync(target)) {
        errors.push(`${path.relative(ROOT, file)}: "${spec}" -> ${path.relative(ROOT, target)} does not exist`);
      }
    }
  }
  assert.ok(files.length > 20, "expected the UI to ship more than a handful of modules");
  assert.ok(checked > 100, `expected the UI to have a real module graph, checked ${checked} specifiers`);
  assert.deepEqual(errors, [], "unresolvable imports:\n" + errors.join("\n"));
});
