# 45. Client unit tests are type-checked TypeScript

**Decided** ([#2090](https://github.com/tkakkie/ribbitto/issues/2090) and
[#2089](https://github.com/tkakkie/ribbitto/issues/2089), approved by the
maintainer on 2026-11-03;
[workflow log](https://github.com/tkakkie/ribbitto/issues/2#issuecomment-6594314637)).
This amends and supersedes in part
[decision 42](42-repository-languages-and-formats.md) and
[decision 43](43-typescript-with-a-go-driven-build.md), which remain as
history.

## Amendment

- **Test harnesses.** Client unit tests are `web/test/<name>.test.ts` directly
  in `web/test/`, type-checked by the existing strict, no-emit TypeScript check
  (`typescript@7.0.2`).
- **Node test declarations.** `web/test/node-test.d.ts` is a hand-written
  declaration file directly in `web/test/`. `@types/node` is not added. The
  declaration file declares only the `node:test` and `node:assert/strict`
  members the tests use, never Node's types as a whole, and is corrected
  minimally when a member is missing or wrong.
- **Type-check inputs.** `tsconfig.json`'s `include` lists
  `web/test/*.test.ts` and `web/test/node-test.d.ts` (named explicitly next to
  `web/test/*.test.ts` and `web/src/**/*.ts`), enforced by
  `tools/typedscripts`. No other `.ts` file is allowed under `web/test/`.
- **Module-scope manifest.** `web/test/package.json` (`{"type": "module"}`) is
  added to the language list as the module-scope manifest for `web/test/`, as
  `web/src/package.json` already is (#1854), so `node --test` loads
  `web/test/*.test.ts` as ES modules without `MODULE_TYPELESS_PACKAGE_JSON`
  reparsing. The root `package.json` module type is not changed.
- **Runner.** Node still runs the harnesses through the Go wrapper
  (`node --test`), with type stripping. Type checking and unit tests remain
  separate required checks.
- **Retiring `.mjs`.** The `web/test/<name>.test.mjs` form is retired. The
  foundation (#2090, #2092, #2091) lands before the remaining M9 conversions so
  they add no new `.test.mjs`. Tooling accepts both `.test.ts` and existing
  `.test.mjs` while #2092–#2095 convert the existing harnesses, and the tools
  (`tools/languagecheck`, `tools/typedscripts`, `tools/webtypes`) stop allowing
  `.mjs` in #2098.

## Unchanged

`node:test`, DOM-free-only unit tests, no DOM emulator, no lint tool, no new
npm dependency, go-rod browser tests, the root `package.json` module type, and
decisions [02](02-server-rendered-html-with-htmx-not-an-spa.md),
[19](19-web-layers-server-owned-html-htmx-swaps-javascript-as-enhancement.md),
[35](35-tooling-in-go.md),
[40](40-plain-javascript-by-default-library-islands-by-measured-exception.md)
and [44](44-remove-the-launcher-language-entries.md).

**Why:** decision 43 started with `.mjs` harnesses and no `@types/node`, to be
reconsidered only when a real need was shown, and decision 42 listed
`web/test/<name>.test.mjs` as the only unit-test form (`tsconfig.json` included
only `web/src/**/*.ts`, required by `tools/typedscripts`). A survey at
`409d680`
([workflow log](https://github.com/tkakkie/ribbitto/issues/2#issuecomment-6594314637))
checked copies of the seven harnesses with the pinned `typescript@7.0.2` and
found 203 errors, almost all missing annotations on mixed tables, and two real
findings: the hand-built `window`/`document` at `drafts.test.mjs:164-165`, and
`isSendDetail`'s parameter type omitting a field the generated contract has
(`drafts.test.mjs:388`, `web/src/drafts.ts:130`, `contracts.gen.ts:178`). No
escaped product bug is attributable to the missing check; the reason is correct
tests and one language.

**Considered:** keeping `.mjs` harnesses without type checking (leaves contract
and signature drift unchecked); a second
`tsc --checkJs --noImplicitAny false` pass over `.mjs` (weaker checking and a
second configuration); adding `@types/node` (unneeded ambient Node surface);
jsdom/happy-dom, Vitest, Playwright, coverage thresholds and Stryker (rejected
by all three AIs in the `409d680` survey).
