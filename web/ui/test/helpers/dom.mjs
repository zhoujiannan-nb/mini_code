// jsdom-backed browser environment shared by every test in a test file.
//
// Import this module *before* "preact" or any component module: component code
// may capture `document`/`window` while it evaluates, and Preact renders into
// whatever `document` exists when `render()` is called.
//
// Each `node --test` file runs in its own child process, so one jsdom window per
// process is enough and there is no cross-file leakage.
import { JSDOM } from "jsdom";

const dom = new JSDOM("<!doctype html><html><body></body></html>", {
  url: "http://localhost/",
  pretendToBeVisual: true,
});

const browserGlobals = [
  "window",
  "document",
  "navigator",
  "location",
  "history",
  "HTMLElement",
  "Element",
  "Node",
  "Text",
  "Comment",
  "DocumentFragment",
  "SVGElement",
  "Event",
  "CustomEvent",
  "MouseEvent",
  "KeyboardEvent",
  "MutationObserver",
  "customElements",
  "getComputedStyle",
  "requestAnimationFrame",
  "cancelAnimationFrame",
  "localStorage",
  "sessionStorage",
];

for (const name of browserGlobals) {
  const value = dom.window[name];
  if (value === undefined) continue;
  Object.defineProperty(globalThis, name, {
    value,
    writable: true,
    configurable: true,
  });
}

export const window = dom.window;
export const document = dom.window.document;

// Preact schedules hook-driven re-renders on a microtask (its `defer` helper)
// and effects on a later animation frame. A macrotask + one frame + a macrotask
// flushes both without needing preact/test-utils' `act`.
export const flush = async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => window.requestAnimationFrame(() => resolve()));
  await new Promise((resolve) => setTimeout(resolve, 0));
};

export default { window, document, flush };
