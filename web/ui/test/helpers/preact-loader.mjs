import { ALIASES } from "./aliases.mjs";

export async function resolve(specifier, context, nextResolve) {
  if (Object.hasOwn(ALIASES, specifier)) {
    return { url: ALIASES[specifier], shortCircuit: true };
  }
  return nextResolve(specifier, context);
}
