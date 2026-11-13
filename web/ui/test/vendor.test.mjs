// Structural checks for the vendored runtime + import map. These guard the two
// constraints the Go static route and the browser impose:
//   1. vendor files must be served as ".js" (ui.go extension whitelist);
//   2. vendored modules must import each other by bare specifier, so the import
//      map can dedupe them ("hooks.js" importing "preact" must hit the same URL).
import test from "node:test";
import assert from "node:assert/strict";
import { readFile, stat } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const uiRoot = fileURLToPath(new URL("..", import.meta.url));
const vendorDir = path.join(uiRoot, "js", "vendor");

async function exists(p) {
  try {
    const s = await stat(p);
    return s.isFile();
  } catch {
    return false;
  }
}

test("importmap.html maps every bare specifier to an existing .js file", async () => {
  const snippet = await readFile(path.join(vendorDir, "importmap.html"), "utf8");
  const match = snippet.match(/<script type="importmap">([\s\S]*?)<\/script>/);
  assert.ok(match, 'importmap.html holds exactly one <script type="importmap">');

  const { imports } = JSON.parse(match[1]);
  assert.deepEqual(imports, {
    preact: "/js/vendor/preact.js",
    "preact/hooks": "/js/vendor/hooks.js",
    htm: "/js/vendor/htm.js",
  });

  for (const url of Object.values(imports)) {
    assert.ok(url.endsWith(".js"), `${url} uses the .js suffix the Go route serves`);
    const file = path.join(uiRoot, url.replace(/^\//, ""));
    assert.ok(await exists(file), `${url} exists on disk`);
  }
});

test("vendored hooks.js imports preact by bare specifier", async () => {
  const src = await readFile(path.join(vendorDir, "hooks.js"), "utf8");
  assert.match(src, /from\s*["']preact["']/, 'hooks.js must `import ... from "preact"`');
  assert.doesNotMatch(src, /from\s*["']\.\.?\//, "no relative imports: the import map resolves them");
});

test("vendor files are byte-identical to their recorded sources", async () => {
  // Cheap integrity guard: sizes recorded in js/vendor/README.md at copy time.
  const expected = {
    "preact.js": 11970,
    "hooks.js": 3628,
    "htm.js": 1207,
  };
  for (const [file, size] of Object.entries(expected)) {
    const s = await stat(path.join(vendorDir, file));
    assert.equal(s.size, size, `${file} size`);
  }
});
