# Web toolchain (planned)

[Decision 43](../decisions/43-typescript-with-a-go-driven-build.md) sets the rules,
including the checker and `.mjs` harnesses. **Proposed** mechanics await maintainer confirmation (PR #1465). #1410–#1415 build the foundation;
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
Specify kinds and exact locations: web/src/*.ts, the chosen .mjs harness
location, root package.json/package-lock.json, .node-version if adopted, and
the Go builder under tools/ (already covered by the Go rule). The language
check fails #1410 or any PR adding unlisted files without the required table entries. Any exact-path exception must follow decision 42's
rules in [`docs/language-exceptions.txt`](../language-exceptions.txt).

The check does not scan Makefile recipes or workflow steps for node/npm command
words; decision 42 leaves commands to review. #1410 must record/review the
runtime commands it uses against the limited approval; commands alone need no
path-and-kind entry. This PR adds no .ts, .mjs, package.json, package-lock.json
or table entry and edits neither decision 42 nor the table.

## Build, manifest and embedding — #1411

**Naming — proposed:** esbuild `[name]-[hash].js` per bundle. Hashes/manifest
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

**Development serving — proposed:** rebuild edits; serve matching disk
bundles/manifest with `no-store`, without rebuilding the binary. Use per-bundle
hashes, no whole-graph prefix.

## Versions and installation — #1410, #1415

**Node pin mechanics — proposed:** #1410
introduces one root `.node-version` containing `26.8.1`. CI setup, local
development and sandbox setup read this same pin; Go wrappers used by one-line
Make recipes reject a different runtime version. Use that Node release's
bundled npm, not a separately upgraded global npm. CI provisions the release
from the pin. For local development and Codex, trusted maintainer setup supplies
it before
the run. Codex downloads no Node and expands no network profile; report missing
or mismatched runtimes. #1415 documents/verifies setup and sandbox-local writes;
it does not supply the toolchain or language-table changes. Argon's command
prohibition and pending verify boundary remain unchanged.

The root `package.json` will pin `typescript` exactly to `7.0.2`, with a
committed `package-lock.json` including optional platform dependencies.
Install only with `npm ci --ignore-scripts` from the reviewed lockfile using
pinned Node; keep install/cache writes inside the implementer's sandbox. No
npm scripts are needed. Go wrappers must prevent inherited npm configuration
from omitting the checker
or platform package or enabling lifecycle scripts. The portable package selects
the executable; no manual platform pins. Classic `6.0.3` replaces it only for a
recorded migration/checker/editor incompatibility. #1410 checks diagnostics and
editor/language-service use on supported platforms; the spike tested no editors.

**Pin updates — proposed:** ordinary
dependency PRs: change the exact checker pin and lockfile together; for a Node
update also change `.node-version` and regenerate the lockfile with that
release's bundled npm. CI must prove installation, type checking and, once
#1412 lands, unit tests with the changed pins.

**Dependabot — proposed:** #1356 says "same as Go"; the twelve points do not
mention it. #1410 adds npm to `.github/dependabot.yml` with Go's seven-day
cooldown; updates still need review and exact pins.

**Syntax — proposed:** erasable TypeScript only: no enums, runtime namespaces
or parameter properties (#1356 proposal, absent from the twelve points).

**Imports — proposed:** relative imports
name `.ts` with `allowImportingTsExtensions` (#1410). Node ignores tsconfig
path aliases. Point 5 decides `.mjs` harnesses without `@types/node`; #1412
runs them against DOM-free `.ts` logic with `node --test`, separately from
type checking.

## Generated contract and state — #1413, #1416, #1417

**Generated-file commitment — proposed:**
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

## Source maps — #1411

Serve development maps locally. Production maps follow
[decision 43](../decisions/43-typescript-with-a-go-driven-build.md)'s approved
private-storage policy; store details are fixed in issue #1469, before #1411. Match maps/bundles by SHA, run id and
content hash; the manifest records SHA/run id.

**Map contents — proposed:** include
`sourcesContent`.

Keep maps out of the image, embedded files and public static routes; no inline
maps or public `sourceMappingURL` link. Match a stack to the exact output.
Verify CSP and delivery in the [target browsers](browser-targets.md), with
version-specific evidence under that document's verification rules. The
classic bundle needs a browser check; module/shared/lazy/preload delivery
needs verification before adoption, without loosening the nonce-only CSP.

## Checks and measurement — #1412, #1414, #1426

Adapt `web/static/javascript_test.go` to inspect authored TypeScript and
remaining JavaScript, allow module-scoped declarations without global writes,
and recognize the converted drafts module's storage ownership. Add TypeScript
syntax fixtures and hashed-manifest checks while preserving forbidden APIs,
requests, storage and HTML-manipulation intent. Regex is no parser or security
boundary; types do not cure alias,
computed-property or multiline blind spots. Keep browser tests.

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

Argon gains no Node/npm commands. Verify remains implementation pending, not
in force, under its existing gates. Its exact Go build/vet/test checklist
excludes generation and TypeScript checking. Packages needing generated assets
stay CI-only (`NOT VERIFIED`); container generation is outside verify and
needs its own `process` issue. #1415 cannot enable verify or widen those
permissions.
