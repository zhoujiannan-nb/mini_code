// Reference component: the canonical "zero-build Preact" pattern for this UI.
//
// - Browser: "preact", "preact/hooks" and "htm" are resolved by the import map
//   in index.html (see js/vendor/importmap.html) to /js/vendor/*.js.
// - Node tests: the same bare specifiers are resolved to the same vendor files
//   by test/helpers/preact-loader.mjs.
// - No JSX, no bundler: htm binds h and parses the tagged template at runtime.
import { h } from "preact";
import { useState } from "preact/hooks";
import htm from "htm";

const html = htm.bind(h);

export function Counter({ label = "count", step = 1 }) {
  const [n, setN] = useState(0);
  return html`<button type="button" class="counter" onClick=${() => setN(n + step)}>${label}: ${n}</button>`;
}

export default Counter;
