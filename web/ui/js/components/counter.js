import { h } from "preact";
import { useState } from "preact/hooks";
import htm from "htm";

const html = htm.bind(h);

export function Counter({ label = "count", step = 1 }) {
  const [n, setN] = useState(0);
  return html`<button type="button" class="counter" onClick=${() => setN(n + step)}>${label}: ${n}</button>`;
}

export default Counter;
