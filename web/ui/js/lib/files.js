export const FS_RECENT_KEY = "mini_code.recent_paths";

export function fsTokenAt(text, caret) {
  const before = String(text || "").slice(0, caret);
  const m = before.match(/@([^\s@]*)$/);
  if (!m) return null;
  const start = caret - m[0].length;
  if (start > 0 && !/\s/.test(text[start - 1])) return null; 
  return { query: m[1], start };
}

export function fsRecent() {
  try { return JSON.parse(localStorage.getItem(FS_RECENT_KEY) || "[]"); } catch (e) { return []; }
}

export function fsRemember(path) {
  try {
    const r = fsRecent().filter((p) => p !== path);
    r.unshift(path);
    localStorage.setItem(FS_RECENT_KEY, JSON.stringify(r.slice(0, 8)));
  } catch (e) {}
}
