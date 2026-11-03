# Web toolchain checks (planned)

Checks and measurement requirements for the [web toolchain](web-toolchain.md).

## Checks and measurement — #1412, #1414, #1426

`web/static/javascript_test.go` inspects every direct `web/src/*.ts` file
(including `.d.ts`) and rejects any direct `web/static/*.js` file;
neither scan descends into subdirectories. An empty TypeScript set fails.
Vendor scripts and generated `dist` bundles are separate categories. The same comment/string
masking and per-line forbidden-API, request, storage, HTML and global-write
rules apply to both. TypeScript permits module-level declarations and exports,
has no versioned-name rule, rejects casts of `window`/`globalThis` that could
hide writes, and has no `localStorage` allowance. #1418 adds the converted
drafts module's storage entry when it creates that module. #1411 owns the
hashed-manifest check. Syntax fixtures preserve the checks' intent; regex is
no parser or security boundary. Types do not cure alias, computed-property,
multiline or interpolation blind spots. Keep review and browser tests.

`make test-web` runs direct `web/test/*.test.ts` harnesses through the existing
Go wrapper's test mode, also in `make check`
([decision 45](../decisions/45-type-checked-client-unit-tests.md); the runner
accepts existing `web/test/*.test.mjs` harnesses and runs both forms from
#2092). Changing a `.test.mjs` harness converts it to `.test.ts` in the same
PR. #2098 starts only when `ls web/test/*.test.mjs` is empty, then removes the
`.mjs` glob. The wrapper checks
`.node-version`, scrubs `NODE_OPTIONS`, expands the glob itself (no matches
fails), needs no `node_modules` and runs `node --test <files>` from the
repository root, propagating failure. `web/src/package.json` and (from #2092)
`web/test/`'s `package.json` (`{"type": "module"}`) scope `web/src/*.ts` and
`web/test/*.test.ts` as ES modules for Node's file-location package lookup
without changing the root `package.json`, avoiding `MODULE_TYPELESS_PACKAGE_JSON`
reparsing (#1854, #2090). Missing/mismatched Node prints `NOT VERIFIED` and skips
unless `RIBBITTO_REQUIRE_NODE=1` makes it fail. CI runs tests after type checking
in `web-types`, even if that check fails, unless cancelled. Hermetic wrapper
tests in `check-tools` (without Node) also check the `web/src/package.json` and
root `package.json` module scope; the real-Node `TestRealNodeTests` probe (only
with that flag) checks pass/fail propagation and fails if
`MODULE_TYPELESS_PACKAGE_JSON` returns (with a temporary-root positive control).
The setup harness creates temporary annotated `.ts` modules and proves relative
`.ts` imports and type stripping. See [`web-tests.md`](web-tests.md) for the
test layers and harness rules.

`tools/typedscripts` runs in `make check-tools`, scanning only `git ls-files`.
Tracked `.js`, `.mjs`, `.cjs` and `.jsx` files must be direct versioned
`web/static/<stem>-vN.js` scripts listed once in `web/legacy-scripts.txt`, with
exactly one file per stem. Exempt paths are the three named vendor scripts
in `web/static/vendor/` (pinned by `vendor_test.go`) and untouched direct
`web/test/*.test.mjs` harnesses (not type-checked; changing one converts it in
the same PR). #2098 removes this allowance only after the empty-glob condition
above is met. On conversion, delete the
old script and its list line together. The list is now empty and may
only shrink against `HEAD^1` when `GITHUB_ACTIONS` is set, otherwise the merge
base with `origin/main`. A missing base list bootstraps from its tracked
versioned scripts. An unreadable base fails CI; locally only the growth rule
prints `NOT VERIFIED`, while all other rules still run. The JSON check requires
exactly `include: ["web/src/**/*.ts"]`, and from #2092
`include: ["web/src/**/*.ts", "web/test/*.test.ts", "web/test/node-test.d.ts"]`,
`strict: true`, `noEmit: true`, `noCheck` absent or `false`, and no `exclude`,
`files` or `extends`; checked keys are case-sensitive. Strict-family
overrides must be `true`. Only `.ts` under `web/src/` and, from #2092, direct
`web/test/*.test.ts` plus `node-test.d.ts` are type-checked; other
TypeScript files fail. Raw-source checks reject `@ts-nocheck` in tracked
`.ts`, `.tsx`, `.mts`, `.cts`, inline script tags without `src` (any case)
and templ script components in every tracked `.templ`. These checks are not
parsers; computed imports and re-exports of untyped data remain for review.

Each issue's pre-`ready` measurement record contains, for all five metrics in
decision 43: baseline SHA, page/path and scenario, device/browser and profile,
method, sample count and aggregation, and numeric allowed head-minus-base
tolerance with units. Include vendor and synchronous scripts in whole-page
totals; report raw bytes too. Use separate response gzip streams (`gzip -9` on
stdin) and state whether request counts are graph counts or browser
observations. Measurement approval, cap, alternating base/head and
quiet-machine rules still follow
[investigations](../workflow/investigations.md) and decision 40 §5. A claimed
unaffected metric needs an explicit method and numeric tolerance, not an
unstated exemption. #1426 sets milestone limits before `ready` too.

