// Compiles one Svelte component with the locked compiler and the declarative
// svelte.config.js; tools/svelte runs it with the pinned Node in web/islands.
import { readFileSync } from "node:fs";
import { compile } from "svelte/compiler";
import config from "./svelte.config.js";

const filename = process.argv[1];
const result = compile(readFileSync(filename, "utf8"), { ...config.compilerOptions, filename, generate: "client" });
for (const warning of result.warnings) console.error(`${filename}: ${warning.code}: ${warning.message}`);
if (result.warnings.length > 0) process.exit(1);
process.stdout.write(result.js.code);
