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

These are approved rules, not an installed toolchain. Implementation is
assigned below; current scripts and checks remain until their replacements
land. The [toolchain page](../architecture/web-toolchain.md) proposes concrete
mechanics, pending maintainer confirmation; PR #1465 lists them in its body.
This decision changes no application behaviour.

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
maintainer approved this storage policy on 2026-10-20 (in chat): the first
choice is a maintainer-only private GitHub repository, with 30-day retention
and automatic deletion, minimal access, and no new external service or
infrastructure. Each map corresponds to its bundle by build SHA, run id and
content hash. A failure to store a map must be detectable, and nothing may let
a map end up in a public artifact. Creating the repository is not part of this
pull request: a separate issue, "Prepare the private source-map store", has it
ready before #1411, which depends on it. The toolchain page proposes whether
maps carry `sourcesContent`.

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

Keep #1404's staged foundation and stateful-first conversions after M7. Before
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

The maintainer's
[approval comment](https://github.com/tkakkie/ribbitto/issues/1356#issuecomment-6289973261)
explicitly approves TypeScript, Node for type checking, tests and dependency
management only, and the Go-driven esbuild build; it is not a blanket
authorization. Number 42 is reserved for the pending language-list decision
(#1437, open draft PR #1441). When that lands, this approval will be entered
in its language list. The language list and enforcement table must change
with the toolchain pull request so code and table agree. Any needed exception
records one exact path,
issue and approving maintainer comment URL per line; this record changes
neither the table nor the exceptions list.

## Boundary with #1361

This decides no client-side cache, optimistic state, upload, request made by a
script on its own, Web Push or service worker. All twelve toolchain rules
above assume nothing about #1361's outcome. That later decision can build on
typed modules holding already-allowed state, generated contracts, the build,
tests and measurement gates without gaining any UX permission here. The
amendment to 19 changes only where already-allowed state lives; any other/new
client-side state (including caches, optimistic states and uploads) and
scripts' own requests are for #1361. It remains free to amend 19, 30, 37 (c)
and 41 afterwards.

## Built later by

- #1410: Node setup, type checking, pins, safe install and npm dependency updates;
  #1411: Go builder, hashed delivery, manifest/embed and map handling.
- #1412: unit tests and source-check adaptation; #1413: generated DOM
  contract; #1414: keep new code typed; #1415: document and verify trusted Node
  provisioning for AI sandboxes, handoffs and measurements.
- #1409: conventions; #1416–#1421: stateful-first conversions;
  #1426: whole-milestone review, measurements and browser verification.

## Trade-offs

A bare `go build` from a fresh clone no longer works directly after the build
migration: generation must precede embedding. Development and CI gain Node and
an npm package with platform dependencies, although build and deployment need
no Node. The native checker's newness and editor compatibility remain
integration risks. Regex checks remain incomplete; size evidence does not
establish browser responsiveness.

**Why:** the existing composer already coordinates state, retries, drafts and
reset across scripts. Typed boundaries and logic tests address the
maintainer's priority of safely growing that code. The spike supports a
Go-only asset build and classic page bundles without justifying a UI library.

**Considered:** JSDoc/no-build (measured baseline, more verbose typed state
and no bundling savings); classic checker first (fallback retained, native
worked in the spike); committed output (fresh-clone convenience at the cost of
generated review diffs); per-platform native packages (manual platform
management); shared chunks and more lint tools (deferred until evidence).
