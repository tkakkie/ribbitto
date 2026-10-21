# 43. TypeScript with a Go-driven front-end build

**Decided** ([#1356](https://github.com/tkakkie/ribbitto/issues/1356), at the
maintainer's request on 2026-10-20;
[direction](https://github.com/tkakkie/ribbitto/issues/1356#issuecomment-6270882217),
[spike
report](https://github.com/tkakkie/ribbitto/issues/1356#issuecomment-6282382453)).
Adopt TypeScript for maintainability, type safety and testability as the front
end grows. New client code is TypeScript by default; existing code converts
stateful-first. JSDoc is the measured comparison baseline, not a migration
target. This supersedes the issue's statement that full TypeScript migration
is not a goal in itself.

## Decided now

These are approved rules, not an installed toolchain; current scripts and
checks remain until the assigned implementations land. The [toolchain page](../architecture/web-toolchain.md) proposes concrete
mechanics, pending maintainer confirmation; PR #1465 lists them in its body.

### Build and delivery

Use esbuild's Go API, with the dependency in `tools/go.mod`, never the
application's module; no esbuild CLI `tool` directive. A Go builder generates
uncommitted `web/static/dist` from ES modules in `web/src/*.ts`. Verify the
manifest and `go:embed` agree. Make, CI, Docker and release procedures must
explicitly generate before compiling; the build itself requires no Node.

Start with one classic IIFE bundle per page kind, content-hashed and selected
through a manifest. The toolchain page proposes the `[name]-[hash].js` naming.
Vendor files and the synchronous `arrival` script stay separate. Shared chunks and dynamic import wait for a measured need; no
whole-graph hash prefix is adopted now.

### Type checking and Node

Use strict, no-emit checking. First choice: the stable native Go checker in
the portable `typescript@7.0.2` npm package verified by the spike, without
manually managing per-platform binaries. Check editor compatibility too. A
demonstrated migration compatibility problem permits classic
`typescript@6.0.3` as the fallback, recorded with the failing case; the two
checkers are not parallel dependencies.

Node 26.8.1 is the initial baseline, pinned reproducibly across CI, the
implementer's sandbox and local development. Record the version-pin mechanism
and update procedure; the toolchain page proposes their mechanics. Node/npm
are limited to type checking, unit tests and dependency management, never the
asset build or run time.

### Tests and lint

Test DOM-free `.ts` logic with Node's test runner. Start with `.mjs` harnesses
and no `@types/node`; reconsider only when a real need is shown. Type checking
and unit tests are separate required checks: Node strips types without
checking them. Keep browser behaviour tests.

No ESLint, Biome or oxlint initially. Use strict TypeScript and improve the
existing Go checks. If a concrete missing rule appears, compare that rule and
candidate tools in a separate issue. The Go check is no security boundary.

### DOM contract

Go is the source of truth for DOM ids, attribute names and event contracts;
generate their TypeScript representation. Generated names alone are not
enough: typed accessors and a Go check must reject stray contract string
literals outside the adapter. State already allowed by decisions 34/37/41 and
UX settings (pending posts, idempotency keys, drafts and bounded unread
recovery state) has its in-memory working copy in typed modules, not
`dataset`; persistence rules in decisions 37/41 are unchanged. JavaScript
owns no application state, never the message list, sidebar or counts, subject
to those existing exceptions. Server attributes and necessary derived DOM
projections remain adapters; any other/new client-side state is for #1361 to
decide. Composer migration must preserve stream reset notifications and
immutable `draftKey` identity across request-key rotation. The toolchain page
proposes generation mechanics and records usage and acceptance requirements.

### Source maps and CSP

Provide development source maps. Production maps are external, private
CI/release artifacts, never public, inlined or in the deployment image. The
maintainer approved private storage of production maps (2026-10-21). The
store's form (for example a repository), access, retention period, deletion
and failure handling are decided in the separate store-preparation issue
(#1469), not here; retention is to be decided from how long a production
release runs and what incident investigation needs. Each map corresponds to
its bundle by build SHA, run id and content hash. A failure to store a map
must be detectable, and nothing may let a map end up in a public artifact. The
store issue precedes #1411, which depends on it.

Keep the nonce-only CSP and verify the initial classic bundle in a browser.
Before ES-module delivery, shared chunks, lazy loading or `modulepreload`,
verify behaviour in the [target browsers](../architecture/browser-targets.md).
Specification reasoning in the spike is not browser verification; never loosen
CSP without verification.

### Existing checks

Adapt `web/static/javascript_test.go` for TypeScript sources, ES-module scope
and the hashed manifest. Preserve the intent of forbidden-API, storage,
request, DOM-manipulation and global-write checks; keep browser tests. Regex
patterns cannot establish semantics or detect every indirect use, even with
types; review and behaviour tests still cover those limits.

### Migration and acceptance

Keep #1404's staged foundation and stateful-first conversions after M7. #1410–#1415 keep the default M9 staged order. Before
any implementation issue is made `ready`, set its measurement method and
numeric tolerance for gzip bytes, request count, input latency, INP and
own-post latency. The details page lists the fields to record; existing
measurement approval and caps still apply. The spike's 32% gzip saving is
build-output evidence, not a real-browser performance gain.

## Amendments and language approval

- [Decision 2](02-server-rendered-html-with-htmx-not-an-spa.md): replace
  no JavaScript build/no npm with the Go-driven build and the limited
  Node/npm uses above. Keep server-rendered HTML, htmx/SSE and the
  no-JavaScript core.
- [Decision 19](19-web-layers-server-owned-html-htmx-swaps-javascript-as-enhancement.md):
  amend only the in-memory working copy of already-allowed state: typed modules,
  not `dataset`; decisions 37/41's persistence rules stay. JavaScript owns no
  application state, never the message list, sidebar or counts, subject to the existing exceptions in decisions
  34/37/41 and UX settings above. Those permissions and the no-requests rule
  stay unchanged; any other/new client-side state is decided by #1361.
- [Decision 35](35-tooling-in-go.md): add exactly one exception, the pinned
  Node type checker and unit-test runner, including their dependency
  management, only for the uses above. The builder, contract generator and
  all check orchestration remain Go under `tools/`; one-line Make recipes stay.
- [Decision 40](40-plain-javascript-by-default-library-islands-by-measured-exception.md):
  TypeScript replaces plain JavaScript as the default source language.
  Keep the island gate and per-feature route (§3), HTML rule (§4) and size
  budget/report (§5). Type annotations add no runtime code and are not a
  library under that gate; this adopts no UI library.

[Decision 42](42-repository-languages-and-formats.md) is merged (#1437/#1441).
Decision 43 accepts the approval through #1356 as decision 42 anticipates. The
maintainer's [approval record](https://github.com/tkakkie/ribbitto/issues/1356#issuecomment-6289973261)
approves TypeScript, Node for type checking, tests and dependency management
only, and the Go-driven esbuild build; it is not blanket authorization.

The language list and path-and-kind table in
[`tools/languagecheck`](../../tools/languagecheck/rules.go) change in the
toolchain PR (#1410, adding build/test files), together with the files and a
reference to that approval, so code and table agree. Each location's table
change belongs to the PR introducing its first file (#1410, #1411, #1412 or
#1413); their acceptance criteria must require it. The kinds and locations are
listed on the toolchain page. The language check will fail any PR adding
unlisted files unless it adds the table entries. Exact-path exceptions use
[`docs/language-exceptions.txt`](../language-exceptions.txt) only under decision
42's rules, with the issue and approving maintainer comment URL.

Decision 42 leaves commands to review: languagecheck does not scan Makefile
recipes or workflow steps for `node`/`npm` command words. #1410 must record and
review its runtime commands against the limited approval; command use alone
needs no path-and-kind entry. This PR adds no `.ts`, `.mjs`, `package.json`,
`package-lock.json` or table entry and edits neither decision 42 nor the table.

## Boundary with #1361

This decides no client-side cache, optimistic state, upload, script-owned
request, Web Push or service worker. All twelve toolchain rules above assume
nothing about #1361's outcome. It can build on the typed modules, contracts,
build, tests and measurement gates without gaining UX permission here. Decision 19 changes only the in-memory location of already-allowed state;
#1361 remains free to amend decisions 19, 30, 37 (c) and 41.

## Built later by

- #1410: Node setup, type checking, pins, safe install and npm dependency updates;
  #1411: Go builder, hashed delivery, manifest/embed and map handling.
- #1412: unit tests and source-check adaptation; #1413: generated DOM
  contract; #1414: keep new code typed; #1415: document and verify trusted Node
  provisioning for AI sandboxes, handoffs and measurements.
- #1409: conventions; #1416–#1421: stateful-first conversions;
  #1426: whole-milestone review, measurements and browser verification.

## Trade-offs

A bare `go build` from a fresh clone no longer works directly after the
build migration: generation must precede embedding. Development and CI gain
Node and an npm package with platform dependencies; build and deployment need
no Node. Native checker/editor compatibility remains a risk. Regex checks are
incomplete; size evidence does not establish browser responsiveness.

**Why:** the existing composer already coordinates state, retries, drafts and
reset across scripts. Typed boundaries and logic tests address the
maintainer's priority of safely growing that code. The spike supports a
Go-only asset build and classic page bundles without justifying a UI library.

**Considered:** JSDoc/no-build (measured baseline, more verbose typed state
and no bundling savings); classic checker first (fallback retained, native
worked in the spike); committed output (fresh-clone convenience at the cost of
generated review diffs); per-platform native packages (manual platform
management); shared chunks and more lint tools (deferred until evidence).
