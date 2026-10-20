# Web toolchain (planned)

[Decision 43](../decisions/43-typescript-with-a-go-driven-build.md) sets the rules,
including the checker and `.mjs` harnesses. Proposals below await maintainer
confirmation (listed in PR #1465). This pull request installs nothing.
#1410–#1415 build the foundation; #1416–#1421 convert scripts. Current versioned JavaScript delivery and source checks apply until
their replacements land. #1409 writes module conventions; this page covers
toolchain mechanics only.

## Build, manifest and embedding — #1411

**Naming — proposed, pending maintainer confirmation:** #1411 uses esbuild
per-bundle `[name]-[hash].js` names. Content hashes and a manifest are decided.
Minify bundles.
A Go registry declares page entries; templ looks up logical names rather
than spelling output filenames.
During conversion, bundle migrated sources without loading their old script
a second time; unchanged scripts keep their current tags and `-vN` names.

The manifest records logical entries, bundle/map filenames and commit SHA.
Embed bundles and the runtime manifest, never maps. Checks reject
missing/unknown registry entries, missing bundles, unexpected output and
paths outside the output tree; prove each referenced bundle is embedded and
named by the pinned builder. Missing generation must fail compilation through
an explicit dist embed or required generated manifest, even if other static
files exist. CI generates into clean output and packages that exact result;
dist is not committed.

Make, CI, Docker and release entry points must state and run Go generation
before Go compilation. Prove generation and embedding work with Node absent
from PATH. Update build instructions with those changes; fresh-clone bare Go
builds lose their current convenience.

**Development serving — proposed, pending maintainer confirmation:** rebuild
edited sources and serve matching disk bundles/manifest with `no-store`,
rather than requiring a binary rebuild on each edit. The initial manifest uses
per-bundle hashes, without a whole-graph prefix.

## Versions and installation — #1410, #1415

**Node pin mechanics — proposed, pending maintainer confirmation:** #1410
introduces one root `.node-version` containing `26.8.1`. CI setup, local
development and sandbox setup read this same pin; Go wrappers used by one-line
Make recipes reject a different runtime version. Use that Node release's
bundled npm, not a separately upgraded global npm. CI provisions the release
from the pin. For local development and Codex, the maintainer's trusted setup
supplies that exact release before the run; Codex does not download Node or
expand its network profile. A missing/mismatched runtime is reported, not
silently replaced. #1415 documents and verifies this setup. Argon's command
prohibition and pending verify boundary remain unchanged.

The root `package.json` will pin `typescript` exactly to `7.0.2`, with a
committed `package-lock.json` including optional platform dependencies.
Install only with `npm ci --ignore-scripts` from the reviewed lockfile using
pinned Node; keep install/cache writes inside the implementer's sandbox. No
npm scripts are needed. The Go wrapper must keep inherited npm configuration
from omitting the checker or its platform package or enabling lifecycle
scripts. The portable package selects the platform executable; no manual
platform pins. Classic `6.0.3` replaces the pin only for a demonstrated
migration checker or editor compatibility failure, with the case and fallback
recorded in its PR. #1410 checks diagnostics and editor/language-service use
on supported development platforms; the spike did not test editors.

**Pin updates — proposed, pending maintainer confirmation:** ordinary
dependency PRs: change the exact checker pin and lockfile together; for a Node
update also change `.node-version` and regenerate the lockfile with that
release's bundled npm. CI must prove installation, type checking and, once
#1412 lands, unit tests with the changed pins.

**Dependabot — proposed, pending maintainer confirmation:** #1356's issue text
proposes the npm cooldown as "same as Go"; the maintainer's twelve points do
not mention Dependabot. Add an npm ecosystem to `.github/dependabot.yml` in
#1410 with Go's current seven-day cooldown. It proposes updates, never
bypasses review or the exact-pin rule.

**Syntax — proposed, pending maintainer confirmation:** #1356 proposes
erasable-only TypeScript (no enums, runtime namespaces or parameter
properties); the maintainer's twelve points do not decide this restriction.

**Imports — proposed, pending maintainer confirmation:** relative imports
name `.ts` with `allowImportingTsExtensions` (#1410). Node ignores tsconfig
path aliases. Point 5 decides `.mjs` harnesses without `@types/node`; #1412
runs them against DOM-free `.ts` logic with `node --test`, separately from
type checking.

## Generated contract and state — #1413, #1416, #1417

**Generated-file commitment — proposed, pending maintainer confirmation:**
#1413 commits web/src/contracts.gen.ts rather than generating it uncommitted.
Go definitions used by templ own ids, attribute names, custom event names and
payload fields/types. A Go generator under `tools/`, reached by `go generate`,
produces constants/types and typed accessors; a Go freshness check fails on
drift. Generated selectors/id helpers also cover dynamic id suffixes. Type
narrowing and runtime validation at the DOM/event boundary remain necessary:
types cannot validate server attributes or third-party htmx event detail by
themselves.

Migrated modules use those accessors for contract hooks and events. No raw
contract selectors, `dataset` keys, attribute names or custom event strings
outside the adapter. A Go check with negative fixtures rejects those literal
bypasses in authored sources; exporting constants alone would let stale
literals type-check. Review covers indirect/computed bypasses the check cannot
prove. Unconverted scripts retain their current contract until their
conversion; the coverage ratchet expands with them (#1414).

Keep server controls/attributes as the boundary and any necessary derived
CSS/test projection. Typed modules hold the in-memory working copy of state
already allowed by decisions 34/37/41 and UX settings: pending posts,
idempotency keys, drafts and bounded unread recovery. Decisions 37/41's
persistence rules stay unchanged. JavaScript owns no application state, never
the message list, sidebar or counts, subject to those existing exceptions. Any
other/new client-side state (caches, optimistic states, uploads) and scripts'
own requests are for #1361. #1416/#1417 must coordinate composer and stream
reset guards: changing queue state must notify/reset the guard even without
DOM mutations. Keep immutable `draftKey` tied to the original draft snapshot,
separate from a 409-rotated retry request key; preserve
stream-first/response-first settlement. Browser tests retain these guarantees,
with contract-specific fixture updates where needed, never weakened
assertions.

## Source maps — #1411

Development maps may be served locally. Production maps follow
[decision 43](../decisions/43-typescript-with-a-go-driven-build.md)'s approved
policy: a maintainer-only private GitHub repository, 30-day retention with
automatic deletion, minimal access, no new service. Each map matches its bundle
by build SHA, run id and content hash; the manifest
records SHA and run id. Failing to store a map must fail visibly, and no step
may put one in a public artifact. A separate store-preparation issue precedes
#1411.

**Map contents — proposed, pending maintainer confirmation:** include
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
requests, storage and HTML-manipulation intent. Regex is a bounded source
check, not parsing or a security boundary; strict types do not cure alias,
computed-property or multiline blind spots. Keep the browser tests.

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

The architecture pages retain today's contracts. Decision 43 changes only
the in-memory working copy of already-allowed state: typed modules replace
authoritative `dataset` state when converted; persistence is unchanged. Server
HTML, the no-JavaScript core, current request ownership and decisions 34/37/41's
permissions remain. The checks transition to typed sources, module scope,
generated contracts and hashed delivery; preserve their intent and browser
tests. Current IIFEs, `-vN` assets and state adapters stay until replaced.

For Codex, no routine Node use until both #1410 (toolchain/setup) and #1415
(documenting and verifying trusted Node provisioning for AI sandboxes) land.
Only then may its sandbox use pinned Node for checking, tests, dependency
management and `npm ci --ignore-scripts` from a reviewed lockfile. Trusted
setup supplies Node; no downloader or network expansion. #1415 also documents
sandbox-local install/cache writes and handoff. No AI runs transferred `node_modules`, npm scripts or Node tests on the host after
handoff; CI remains the check.

Argon gains no Node/npm commands. Verify remains implementation pending, not
in force, under its existing gates. Its exact Go build/vet/test checklist
excludes generation and TypeScript checking. Packages needing generated assets
stay CI-only (`NOT VERIFIED`); container generation is outside verify and
needs its own `process` issue. #1415 cannot enable verify or widen those
permissions.
