import test from "node:test";
import assert from "node:assert/strict";

import { document, flush } from "./helpers/dom.mjs";
import { h, render } from "preact";
import htm from "htm";
import { Counter } from "../js/components/counter.js";

const html = htm.bind(h);

function mount() {
  const root = document.createElement("div");
  document.body.appendChild(root);
  render(html`<${Counter} label="clicks" />`, root);
  return root;
}

test("smoke: renders a Preact component built with htm", () => {
  const root = mount();
  assert.ok(root.querySelector("button.counter"), "button is in the DOM");
  assert.equal(root.textContent, "clicks: 0");
});

test("smoke: state updates re-render the DOM", async () => {
  const root = mount();
  const button = root.querySelector("button");

  button.click();
  await flush();
  assert.equal(root.textContent, "clicks: 1", "first click is reflected");

  button.click();
  await flush();
  assert.equal(root.textContent, "clicks: 2", "second click is reflected");
});

test("smoke: loader alias and #preact subpath import are the same module", async () => {
  const viaLoader = await import("preact");
  const viaSubpath = await import("#preact");
  assert.equal(viaLoader, viaSubpath, "both resolve to js/vendor/preact.js");
});
