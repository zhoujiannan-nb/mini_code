/* ---------------- 基础 ---------------- */
async function api(path, opts) {
  const r = await fetch(path, opts);
  if (!r.ok) {
    let msg = r.status + " " + r.statusText;
    try { const j = await r.json(); if (j.error) msg = j.error; } catch (e) {}
    throw new Error(msg);
  }
  return r.json();
}
/* esc() escapes for BOTH text and attribute contexts. The old
   textContent->innerHTML trick only handled & < >, so any value dropped into
   an attribute ("title=\"" + esc(x) + "\"") could close the attribute and
   inject markup. Escaping the quote characters too is invisible in text
   (browsers decode the entities) and safe in attributes. */
function esc(s) {
  return String(s == null ? "" : s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}
