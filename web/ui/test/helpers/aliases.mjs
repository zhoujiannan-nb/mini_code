const vendor = (file) => new URL(`../../js/vendor/${file}`, import.meta.url).href;

export const ALIASES = Object.freeze({
  preact: vendor("preact.js"),
  "preact/hooks": vendor("hooks.js"),
  htm: vendor("htm.js"),
});

export default ALIASES;
