# Web toolchain checks (planned)

Checks and measurement requirements for the [web toolchain](web-toolchain.md).

## Checks and measurement — #1412, #1414, #1426

`web/static/javascript_test.go` inspects every direct `web/src/*.ts` file
(including `.d.ts`) and every direct `web/static/*.js` file, regardless of
version; neither scan descends into subdirectories. Both sets are checked;
missing application JavaScript still fails until #1831. The same comment/string
masking and per-line forbidden-API, request, storage, HTML and global-write
rules apply to both. TypeScript permits module-level declarations and exports,
has no versioned-name rule, rejects casts of `window`/`globalThis` that could
hide writes, and has no `localStorage` allowance. #1418 adds the converted
drafts module's storage entry when it creates that module. #1411 owns the
hashed-manifest check. Syntax fixtures preserve the checks' intent; regex is
no parser or security boundary. Types do not cure alias, computed-property,
multiline or interpolation blind spots. Keep review and browser tests.

`make test-web` runs direct `web/test/*.test.mjs` harnesses through the existing
Go wrapper's test mode, also in `make check`. It checks `.node-version`, scrubs
`NODE_OPTIONS`, expands the glob itself (no matches fails), needs no
`node_modules` and runs `node --test <files>` from the repository root,
propagating failure. Missing/mismatched Node prints `NOT VERIFIED` and skips
unless `RIBBITTO_REQUIRE_NODE=1` makes it fail. CI runs tests after type checking
in `web-types`, even if that check fails, unless cancelled. Hermetic wrapper
tests run in `check-tools` without Node; the real-Node pass/fail probe runs only
with that flag. The sole setup harness creates temporary annotated `.ts`
modules and proves relative `.ts` imports and type stripping. Future harnesses
import DOM-free `web/src/<concern>.ts` by relative `.ts` path, using `node:test`
and `node:assert`, without Node typings or a DOM emulator. Harnesses are outside
tsconfig's inputs: stripping types is not checking them, so both checks remain
required.

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

