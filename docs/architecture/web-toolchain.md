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
package.json/package-lock.json and .node-version. Later PRs add the .mjs harness
location and the Go builder under tools/ (already covered by the Go rule). The language
check fails #1410 or any PR adding unlisted files without the required table entries. Any exact-path exception must follow decision 42's
rules in [`docs/language-exceptions.txt`](../language-exceptions.txt).

The check does not scan Makefile recipes or workflow steps for node/npm command
words; decision 42 leaves commands to review. #1410 must record/review the
runtime commands it uses against the limited approval; commands alone need no
path-and-kind entry. #1410 adds the TypeScript/configuration files and updates
decision 42 and the table; .mjs remains deferred to #1412.

## Build, manifest and embedding — #1411

**Naming — confirmed:** esbuild `[name]-[hash].js` per bundle. Hashes/manifest
are decided. Minify. A Go registry declares page entries; templ uses logical
names. Load converted bundles without their old script; unchanged scripts keep
their tags and `-vN` names.

The manifest records logical entries, bundle/map filenames and commit SHA.
Embed bundles and runtime manifest, never maps. Reject missing/unknown entries,
missing bundles, unexpected output and paths outside the output tree; prove
referenced bundles are embedded and named by the pinned builder. Missing
generation must fail compilation through
an explicit dist embed or required generated manifest, even if other static
files exist. CI generates into clean output and packages that exact result;
dist is not committed.

Make, CI, Docker and release entry points must document/run Go generation
before compilation. Prove generation/embedding work without Node in PATH;
update build instructions, including fresh-clone generation.

**Development serving — confirmed:** rebuild edits; serve matching disk
bundles/manifest with `no-store`, without rebuilding the binary. Use per-bundle
hashes, no whole-graph prefix.

## Versions and installation — #1410, #1415

**Node pin mechanics — confirmed:** one root `.node-version` containing `26.8.1`. CI setup, local
development and sandbox setup read this same pin; Go wrappers used by one-line
Make recipes reject a different runtime version. Use that Node release's
bundled npm, not a separately upgraded global npm. CI provisions the release
from the pin. For local development and Codex, trusted maintainer setup supplies
it before
the run. Codex downloads no Node and expands no network profile; report missing
or mismatched runtimes. #1415 documents/verifies setup and sandbox-local writes;
it does not supply the toolchain or language-table changes. Argon's command
prohibition and verify boundary remain unchanged.

The root `package.json` pins `typescript` exactly to `7.0.2`, with a
committed `package-lock.json` including optional platform dependencies.
Install only with `npm ci --ignore-scripts` from the reviewed lockfile using
pinned Node; keep install/cache writes inside the implementer's sandbox. No
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
then runs `go -C tools test -count=1 ./webtypes -run '^TestRealTypeScript$'`
(the ill-typed TempDir input must produce TS2322). Hermetic wrong-Node and
lockfile tests run in `check-tools` in `checks`, without Node. The strict,
no-emit config includes only web/src/**/*.ts; existing versioned scripts stay
outside it. `globals.d.ts` supplies the first input and the htmx global.

**Pin updates — confirmed:** ordinary
dependency PRs: change the exact checker pin and lockfile together; for a Node
update also change `.node-version` and regenerate the lockfile with that
release's bundled npm. CI must prove installation, type checking and, once
#1412 lands, unit tests with the changed pins.

**Dependabot — confirmed:** #1410 adds npm to `.github/dependabot.yml` with Go's seven-day
cooldown; updates still need review and exact pins.

**Syntax — confirmed:** erasable TypeScript only: no enums, runtime namespaces
or parameter properties.

**Imports — confirmed:** relative imports
name `.ts` with `allowImportingTsExtensions` (#1410). Node ignores tsconfig
path aliases. Point 5 decides `.mjs` harnesses without `@types/node`; #1412
runs them against DOM-free `.ts` logic with `node --test`, separately from
type checking.

## Generated contract and state — #1413, #1416, #1417

**Generated-file commitment — confirmed:**
#1413 commits web/src/contracts.gen.ts rather than generating it uncommitted.
Go definitions used by templ own ids, attribute names, custom event names and
payload fields/types. A Go generator under `tools/`, reached by `go generate`,
produces constants/types and typed accessors; a Go freshness check fails on
drift. Generated selectors/id helpers also cover dynamic id suffixes. Runtime
validation/type narrowing remain necessary: types cannot validate
server attributes or htmx event detail.

Migrated modules use those accessors for contract hooks and events. No raw
contract selectors, `dataset` keys, attribute names or custom event strings
outside the adapter. A Go check with negative fixtures rejects those literal
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

For Codex, no routine Node use until both #1410 (toolchain/setup) and #1415
(documenting and verifying trusted Node provisioning for AI sandboxes) land.
Only then may its sandbox use pinned Node for checking, tests, dependency
management and `npm ci --ignore-scripts` from a reviewed lockfile. Trusted
setup supplies Node; no downloader or network expansion. #1415 also documents
sandbox-local install/cache writes and handoff. No AI runs transferred
`node_modules`, npm scripts or Node tests on the host after
handoff; CI remains the check.
The wrapper's `node --version` probe in `make check` is not routine Node use.

**One-time Codex exception (#1410 only, maintainer 2026-10-25):** trusted host
Node 26.8.1 may run inside the normal implementation profile, without a Node
download or network expansion. `npm install` is used only for lockfile creation
or regeneration; `npm ci` verifies the lock. Both require `--ignore-scripts`,
solely to generate/verify the first lock here, with cache in `bin/.cache/npm`.
Delete `node_modules` before handoff; afterwards no AI runs it, npm scripts or
Node tests on the host. CI is the check. This exception ends when #1410's last
PR merges; routine use still waits for #1415 too.

Argon gains no Node/npm commands. Verify, in force since 2026-10-21, stays
under its existing gates. The [regeneration and generated checks](../workflow/argon-verify-image.md)
(#1696) are merged but not in force until #1700: production still pins launcher
`argonreview-7f607ca` and the old image. The prelude regenerates templ, sqlc and Tailwind
before Go build/vet/test; `generated` checks output and formatting in a copy.
TypeScript checking stays outside verify.
#1415 cannot widen verify or those permissions.

[cf]: https://github.com/tkakkie/ribbitto/issues/1356#issuecomment-6311498388
