// Bare specifier -> vendored ESM file. Single source of truth for the Node test
// loader; the browser gets the same mapping from js/vendor/importmap.html.
const vendor = (file) => new URL(`../../js/vendor/${file}`, import.meta.url).href;

export const ALIASES = Object.freeze({
  preact: vendor("preact.js"),
  "preact/hooks": vendor("hooks.js"),
  htm: vendor("htm.js"),
});

export default ALIASES;
