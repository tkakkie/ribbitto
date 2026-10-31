# Web toolchain

[Decision 43](../decisions/43-typescript-with-a-go-driven-build.md) sets the rules,
including the checker and `.mjs` harnesses. Mechanics marked **confirmed** were confirmed by the maintainer on 2026-10-21 ([record][cf]). #1410–#1415 build the foundation;
#1416–#1421 convert scripts; #1409 writes module conventions. Current delivery
and checks apply until replaced.

## Language approval — #1410–#1413

[Decision 42](../decisions/42-repository-languages-and-formats.md) is merged.
Decision 43 accepts approval through #1356 as decision 42 anticipates: the
maintainer's [approval record](https://github.com/tkakkie/ribbitto/issues/1356#issuecomment-6289973261)
covers TypeScript, Node only for type checking/tests/dependency management, and the
Go-driven esbuild build, not blanket authorization.

#1410 adds build/test files together with the language-list and path-and-kind
table changes in [`tools/languagecheck`](../../tools/languagecheck/rules.go),
referencing that approval. Acceptance criteria for #1410 and #1411/#1412/#1413
must require each location's table change in the PR introducing its first file.
Approved in #1410: web/src/*.ts (including declarations), root tsconfig.json,
package.json/package-lock.json and .node-version. The client unit-test harnesses are direct web/test/*.test.mjs files (#1412);
#1411 adds the Go builder under tools/ (covered by the Go rule). The language
check fails #1410 or any PR adding unlisted files without the required table entries. Any exact-path exception must follow decision 42's
rules in [`docs/language-exceptions.txt`](../language-exceptions.txt).

The check does not scan Makefile recipes or workflow steps for node/npm command
words; decision 42 leaves commands to review. #1410 must record/review the
runtime commands it uses against the limited approval; commands alone need no
path-and-kind entry. #1410 adds the TypeScript/configuration files and updates
decision 42 and the table; #1412 adds the narrow .mjs harness row.

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

First entry: `web/src/message-time.ts`. Its legacy versioned script
remains unchanged and loaded. Output is neither embedded nor served: #1820
adds the registry, templ selection, embedding, serving, browser verification
and generation before compilation in Make, CI, Docker and releases, including
fresh-clone instructions and failure on missing generation. #1821 adds rebuilds,
disk serving with `no-store` and development maps, whose file names it records
in the manifest. #1819 adds private production maps and their separate index;
production map names never belong in the runtime manifest.

## Versions and installation — #1410, #1415

**Node pin mechanics — confirmed:** one root `.node-version` containing `26.8.1`. CI setup, local
development and sandbox setup read this same pin; Go wrappers used by one-line
Make recipes reject a different runtime version. Use that Node release's
bundled npm, not a separately upgraded global npm. The wrapper resolves npm from
the directory of the version-checked Node without resolving Node symlinks. CI provisions the release
from the pin. For local development and Codex, trusted maintainer setup supplies
it before
the run. Codex downloads no Node and expands no network profile; report missing
or mismatched runtimes. [Codex Node setup and probe](../workflow/codex-node.md)
record trusted provisioning and sandbox-local writes. Argon's command
prohibition and verify boundary remain unchanged.

The root `package.json` pins `typescript` exactly to `7.0.2`, with a
committed `package-lock.json` including optional platform dependencies.
Install only with `npm ci --ignore-scripts` from the reviewed lockfile using
pinned Node; keep install/cache writes inside the implementer's sandbox,
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

Routine Codex Node use starts at #1415's merge, after #1410, under the
[setup and sandbox rules](../workflow/codex-node.md). The one-time #1410
exception ended with its last PR; it is not routine authorization.
The wrapper's `node --version` probe in `make check` is not routine Node use.
Handoff follows the [standing prohibition](../workflow/running-other-ai.md#handoff-of-another-ais-code).

Argon gains no Node/npm commands. Verify, in force since 2026-10-21, stays
under its existing gates. The [regeneration and generated checks](../workflow/argon-verify-image.md)
(#1696) are in force since 2026-10-27 (#1700 stage A): production pins launcher
`argonreview-2e3a6bf`. The prelude regenerates
templ, sqlc and Tailwind before Go build/vet/test; `generated` checks output
and formatting in a copy.
TypeScript checking stays outside verify.
#1415 cannot widen verify or those permissions.

[cf]: https://github.com/tkakkie/ribbitto/issues/1356#issuecomment-6311498388
