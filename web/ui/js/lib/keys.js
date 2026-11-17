let seq = 0;

export function newKey(prefix) {
  seq += 1;
  return (prefix || "k") + "-" + seq;
}

export function resetKeys() {
  seq = 0;
}
