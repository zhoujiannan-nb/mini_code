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

export const flush = async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => window.requestAnimationFrame(() => resolve()));
  await new Promise((resolve) => setTimeout(resolve, 0));
};

export default { window, document, flush };
