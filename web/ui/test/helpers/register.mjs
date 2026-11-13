// Entry point for `node --import ./test/helpers/register.mjs ...`.
// Registers the resolve hook before any test module (and therefore before
// "preact"/"htm") is loaded.
import { register } from "node:module";

register("./preact-loader.mjs", import.meta.url);
