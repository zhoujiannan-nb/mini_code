// Resolve hook: teaches Node the same bare specifiers the browser import map
// defines, pointing at the checked-in vendor files. Kept deliberately tiny.
import { ALIASES } from "./aliases.mjs";

export async function resolve(specifier, context, nextResolve) {
  if (Object.hasOwn(ALIASES, specifier)) {
    return { url: ALIASES[specifier], shortCircuit: true };
  }
  return nextResolve(specifier, context);
}
