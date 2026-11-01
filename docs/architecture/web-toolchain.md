# Web toolchain

[Decision 43](../decisions/43-typescript-with-a-go-driven-build.md) sets the rules,
including the checker and `.mjs` harnesses. The maintainer confirmed mechanics
marked **confirmed** on 2026-10-21
([record][cf]). #1410–#1415 build the foundation; #1416–#1421 convert scripts;
#1409 writes module conventions.

## Language approval — #1410–#1413

[Decision 42](../decisions/42-repository-languages-and-formats.md) is merged.
Decision 43 accepts approval through #1356 as decision 42 anticipates: the
maintainer's [approval record](https://github.com/tkakkie/ribbitto/issues/1356#issuecomment-6289973261)
covers TypeScript, Node only for type checking/tests/dependency management, and the
Go-driven esbuild build, not blanket authorization.

#1410 approved web/src/*.ts (including declarations), root tsconfig.json,
package.json/package-lock.json and .node-version, updating decision 42 and
[`tools/languagecheck`](../../tools/languagecheck/rules.go) against that approval.
Later PRs add rows to both path-and-kind tables in the PR introducing the
first file of a new kind or location: direct web/test/*.test.mjs harnesses are
#1412's; Go builders under tools/ are covered. Unlisted files fail; exact-path
exceptions follow decision 42's rules in [`docs/language-exceptions.txt`](../language-exceptions.txt).

Review checks Makefile/workflow node/npm commands against that limited approval
(decision 42 leaves commands to review); the check does not scan them and they
need no path-and-kind entries. #1412 adds the narrow .mjs row.

## Build, manifest and embedding — #1411

**Naming — confirmed:** `make bundles` runs `tools/webbuild` via esbuild's Go API, pinned only in
the tools module. Each named `web/src/<name>.ts` entry becomes a minified
classic IIFE named by esbuild's `[name]-[hash].js`, without shared chunks or
dynamic imports. ES2017 is the syntax target approved by the maintainer on
2026-10-26 and recorded in [browser target rule 4](browser-targets.md#design-rules).
It adds no Web API polyfills and does not constitute browser verification.
Browser verification of the served bundle belongs to #1820.

The builder validates esbuild's metafile and output, writes
`manifest.json` last, then replaces the whole ignored `dist` directory
under `web/static`. Logical entries record bundle filenames and SHA-256 content hashes;
commit SHA and run id are inputs. Make passes HEAD and `GITHUB_RUN_ID`, defaulting
locally to a UTC timestamp. Production output has no maps or source-map links.

`static.NewScripts` in `web/static/scripts.go` holds the single `names`
declaration of converted page entries (`message-time` and `composer`; a conversion
adds its entry name there) and validates the manifest and bundle hashes at
startup; `tools/webbuild` reads that same declaration from the repository root
instead of taking entry names on the command line (#2022). Non-entry modules
under `web/src/` are never built as bundles on their own. templ uses context;
direct renders use the embedded registry without mutable globals. Their legacy
assets and `web/legacy-scripts.txt` entries are removed; the 15 remaining stems
stay on versioned assets. Missing/unknown entries or bundles fail startup;
undeclared renders fail.
Classic bundle: deferred, with nonce; arrival stays synchronous.
Registered hashed `/static/dist/` bundles are immutable; the manifest is private.

Fresh checkout: `make bundles` before direct `go build ./...`, `go test`,
`go vet`, `go run` or tools loading the tree. Make build/check/run, CI schema
verification, testshard and CodeQL generate first. Embed patterns require
generation; ignored `dist` is never committed. CI checks generation/embedding/build
without Node on `PATH`. Future Docker/release workflows must generate before
compiling and package that output (none exists).
Host builds use the
[writable-copy routes](../workflow/investigations.md#build-routes).

In `make dev`, `cmd/dev` runs `webbuild -dev` and supervises `webbuild -watch`,
writing development bundles, `.js.map` files and `manifest.json` to `bin/dev-dist`
without touching `web/static/dist`; recent generations stay on disk and
`manifest.json` is replaced atomically. `RIBBITTO_DEV_ASSETS=bin/dev-dist`
re-reads the manifest once per request and serves `web/static` assets and
`bin/dev-dist` bundles and `.js.map` files with `Cache-Control: no-store` (#1821).
#1819 adds private production maps, absent from the runtime manifest. ES modules,
shared chunks, lazy loading and `modulepreload` need target-browser verification
before adoption.

## Versions and installation — #1410, #1415

**Node pin mechanics — confirmed:** one root `.node-version` containing `26.8.1`. CI and local
development read this same pin; Go wrappers used by one-line
Make recipes reject a different runtime version. Use that Node release's
bundled npm, not a separately upgraded global npm. The wrapper resolves npm from
the directory of the version-checked Node without resolving Node symlinks. CI provisions the release
from the pin. For local development and Codex, trusted maintainer setup supplies
it before
the run. Report missing or mismatched runtimes. Any AI may run the pinned Node
and npm like any other command ([workflow rules](../workflow/README.md), #2002).

The root `package.json` pins `typescript` exactly to `7.0.2`, with a
committed `package-lock.json` including optional platform dependencies.
Install only with `npm ci --ignore-scripts` from the reviewed lockfile using
pinned Node; keep install/cache writes in the worktree,
with the npm cache in `bin/.cache/npm`. No
npm scripts are needed. Go wrappers must prevent inherited npm configuration
from omitting the checker
or platform package or enabling lifecycle scripts. The portable package selects
the executable; no manual platform pins. Classic `6.0.3` replaces it only for a
recorded migration/checker/editor incompatibility. #1410 checks CLI diagnostics
on macOS arm64 and Linux x64; editor/language-service evidence is deferred to
#1815 by the maintainer (2026-10-26). The spike tested no editors.

Run `make web-install` explicitly, then `make check-web`; checks never install.
`make check` and `make prepush` print `NOT VERIFIED` and skip when pinned Node
or the checker is missing/mismatched, unless `RIBBITTO_REQUIRE_NODE=1` makes
that fail. CI's separate `web-types` job installs and checks with that flag,
then runs the real checker/runner tests with `go -C tools test -count=1 -v ./webtypes ./domcontract -run '^TestReal(TypeScript|ContractTypeScript|NodeTests)$'`
(the ill-typed TempDir input must produce TS2322; the second test also checks
that the generated sidebar contract's valid consumers compile and its
ill-typed consumers produce the expected diagnostics). Hermetic wrong-Node and
lockfile tests run in `check-tools` in `checks`, without Node. The strict,
no-emit config includes only web/src/**/*.ts; scripts outside it are bounded by
`web/legacy-scripts.txt` (#1414). `globals.d.ts` supplies the first input and the htmx global.

**Pin updates — confirmed:** in a reviewed pin-update PR, change the exact
checker pin and lockfile together; for a Node update also change `.node-version`.
When a pin or lockfile changes, regenerate `package-lock.json` with the pinned
Node release's bundled npm: `npm install --ignore-scripts` is used only for
lockfile creation or regeneration; `npm ci --ignore-scripts` verifies the lock.
CI must prove installation, type checking and unit tests with the changed pins.

**Dependabot — confirmed:** #1410 adds npm to `.github/dependabot.yml` with Go's seven-day
cooldown; updates still need review and exact pins.

**Syntax — confirmed:** erasable TypeScript only: no enums, runtime namespaces
or parameter properties.

**Imports — confirmed:** relative imports
name `.ts` with `allowImportingTsExtensions` (#1410). Node ignores tsconfig
path aliases. Direct `web/test/*.test.mjs` harnesses use `node:test` and
`node:assert` without `@types/node` and import DOM-free `.ts` logic by relative
`.ts` path. `make test-web` runs them with `node --test`, separately from type
checking and not type-checked itself ([checks](web-toolchain-checks.md)).

## Generated contract and state — #1413, #1416, #1417

**Generated-file commitment — confirmed:**
#1413 commits web/src/contracts.gen.ts rather than generating it uncommitted.
Go definitions used by templ own ids, attribute names, custom event names and
payload fields/types. A Go generator under `tools/`, reached by `go generate`,
produces constants/types and typed accessors; a Go freshness check fails on
drift. Generated selectors/id helpers also cover dynamic id suffixes. #1823 adds
the composer contract. Runtime validation/type narrowing remain in the modules:
types cannot validate server attributes or htmx event detail.

Migrated modules use those accessors for contract hooks and events. No raw
contract selectors, `dataset` keys, attribute names or custom event strings
outside the adapter. A Go check with negative fixtures (#1822) rejects those literal
bypasses in authored sources; exporting constants alone would let stale
literals type-check. Review covers indirect/computed bypasses the check cannot
prove. Unconverted scripts retain their current contract until their
conversion; the coverage ratchet expands with them (#1414).

Keep server controls/attributes and necessary derived CSS/test projections.
Typed modules hold the working copy of already-allowed state: pending posts,
idempotency keys, drafts and bounded unread recovery (decisions 34/37/41 and
UX settings). Persistence stays unchanged. JavaScript owns no application
state, never the message list, sidebar or counts, subject to those exceptions.
Other/new state and script-owned requests are for #1361. #1416/#1417 must
coordinate composer and stream
reset guards: changing queue state must notify/reset the guard even without
DOM mutations. Keep immutable `draftKey` tied to the original draft snapshot,
separate from a 409-rotated retry request key; preserve
stream-first/response-first settlement. Browser tests retain these guarantees,
with contract-specific fixture updates where needed, never weakened
assertions.

See [source maps](web-source-maps.md#source-maps--1411).

See [checks and measurement](web-toolchain-checks.md#checks-and-measurement--1412-1414-1426).

## Transition from current rules — #1410–#1421

Preserve current contracts, server HTML, the no-JavaScript core, request
ownership, persistence and decisions 34/37/41's permissions. On conversion,
typed modules replace authoritative `dataset` working state. Preserve check
intent and browser tests; IIFEs, `-vN` assets and adapters stay until replaced.

Routine Node use starts at #1415's merge, after #1410; the one-time #1410
exception ended with its last PR.
The wrapper's `node --version` probe in `make check` is not routine Node use.

[cf]: https://github.com/tkakkie/ribbitto/issues/1356#issuecomment-6311498388
