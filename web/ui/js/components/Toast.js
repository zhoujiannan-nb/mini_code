import { h } from "preact";
import { useEffect } from "preact/hooks";
import htm from "htm";

import { useStore, dismissToast } from "../lib/store.js";

const html = htm.bind(h);

export function Toast() {
  const msg = useStore((s) => s.toast);
  useEffect(() => {
    if (!msg) return undefined;
    const t = setTimeout(() => dismissToast(msg.id), 4200);
    return () => clearTimeout(t);
  }, [msg]);
  return html`<div id="toast" class=${msg ? "show " + (msg.kind || "") : ""}>${msg ? msg.msg : ""}</div>`;
}
