# Vendored Preact runtime (no build step)

These are **verbatim copies** of the ESM builds from `web/ui/node_modules`. They
are checked in so the Go binary can embed and serve them through the static asset
route (`/js/vendor/*.js`, see `web/ui.go` → `loadUIAssets`). The route only
whitelists certain extensions, so every file here **must keep the `.js` suffix**
(the upstream `.mjs` files are renamed on copy; contents are unchanged).

The browser resolves the bare specifiers through the import map in
[`importmap.html`](./importmap.html) — see `index.html`'s `<head>`.

## Files

| Vendor file | npm package | Version | Upstream file | Bytes | License |
| --- | --- | --- | --- | ---: | --- |
| `preact.js` | `preact` | **11.2.0** | `node_modules/preact/dist/preact.mjs` | 11970 | MIT |
| `hooks.js` | `preact` (`preact/hooks`) | **11.2.0** | `node_modules/preact/hooks/dist/hooks.mjs` | 3628 | MIT |
| `htm.js` | `htm` | **3.1.1** | `node_modules/htm/dist/htm.module.js` | 1207 | Apache-2.0 |

Build-layout note: Preact **11** ships `dist/preact.mjs` and
`hooks/dist/hooks.mjs`; there is no `preact.module.js` / `hooks.module.js`
anymore (the `package.json` `exports` map points at the `.mjs` files above).
`htm` still ships `dist/htm.module.js`, so the original path was used for it.

All three copies are byte-identical to the upstream files they were copied from:

| File | SHA-256 |
| --- | --- |
| `preact.js` | `BDCBD9ABDF2C77C5C45326EAB42191E949222D479D0D5F69467387A77040CB55` |
| `hooks.js` | `E862AB2973AA6455EAA0F77C5D832D2157D6BE59A17C8BA546550A575B58C6B4` |
| `htm.js` | `AB33DD3F38059B9BE4D5F5350128EEFB2356639C4E0BBE9D9E8B3BA75847E9E4` |

`test/vendor.test.mjs` asserts the byte sizes and the import map URLs, so a
half-finished upgrade fails the test suite.

## Bare specifiers

- `hooks.js` starts with `import{options as n}from"preact";` — the bare
  specifier the import map maps back to `preact.js`, so hooks and components
  share **one** Preact instance (`options` hooks work).
- `preact.js` has no imports at all; `htm.js` has no imports at all.
- Do not edit the vendored files. Never vendor a file that imports a relative
  path (`./x.js`), because the served layout (`/js/vendor/...`) is not the
  upstream layout.

## Licenses

- Preact (core + hooks): MIT — full text in [`LICENSE-preact.txt`](./LICENSE-preact.txt).
- htm: **Apache-2.0** (not MIT) — full text in [`LICENSE-htm.txt`](./LICENSE-htm.txt).

Both license files were copied from the corresponding `node_modules` packages.

## Import map

`importmap.html` is the exact snippet to paste into `<head>`:

```html
<script type="importmap">{"imports":{"preact":"/js/vendor/preact.js","preact/hooks":"/js/vendor/hooks.js","htm":"/js/vendor/htm.js"}}</script>
```

It must appear **before any `<script type="module">`** (the HTML spec ignores an
import map that is parsed after a module script has started fetching). It is not
a served file: it is a snippet file (`.html` is not in the Go asset whitelist).

## Upgrading

```powershell
cd web/ui
npm install --save-exact preact@<version> htm@<version>
Copy-Item node_modules/preact/dist/preact.mjs        js/vendor/preact.js -Force
Copy-Item node_modules/preact/hooks/dist/hooks.mjs  js/vendor/hooks.js -Force
Copy-Item node_modules/htm/dist/htm.module.js       js/vendor/htm.js -Force
Copy-Item node_modules/preact/LICENSE               js/vendor/LICENSE-preact.txt -Force
Copy-Item node_modules/htm/LICENSE                  js/vendor/LICENSE-htm.txt -Force
npm test
```

Then update the version/byte/SHA-256 tables above and the sizes in
`test/vendor.test.mjs`. If a future release renames the ESM entry again, check
the package's `exports` / `module` field and record the actual file in the table.

## Source map comments

`preact.js` and `hooks.js` keep the upstream `//# sourceMappingURL=...mjs.map`
comment. The `.map` files are not vendored (and `.map` is not served), so
devtools will show a harmless 404 for the map. Strip those two comment lines
only if that noise matters; otherwise keep the copies verbatim.
