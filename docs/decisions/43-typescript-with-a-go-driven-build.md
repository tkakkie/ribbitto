# 43. TypeScript with a Go-driven front-end build

**Decided** ([#1356](https://github.com/tkakkie/ribbitto/issues/1356), at the
maintainer's request on 2026-10-20;
[direction](https://github.com/tkakkie/ribbitto/issues/1356#issuecomment-6270882217),
[spike report](https://github.com/tkakkie/ribbitto/issues/1356#issuecomment-6282382453)).
Adopt TypeScript for maintainability, type safety and testability as the
front end grows. New client code is TypeScript by default; existing code
converts stateful-first. JSDoc is the measured comparison baseline, not a
migration target. This supersedes the issue's statement that full
TypeScript migration is not a goal in itself.

## Decided now

These are approved rules, not an installed toolchain. Implementation is
assigned below; current scripts and checks remain until their replacements
land. [Toolchain details](../architecture/web-toolchain.md) specify the
mechanics without changing application behaviour in this decision.

### Build and delivery

Use esbuild's Go API, with the dependency in `tools/go.mod`, never the
application's module; no esbuild CLI `tool` directive. A Go builder generates
uncommitted `web/static/dist` from ES modules in `web/src/*.ts`. Verify the
manifest and `go:embed` agree. Make, CI, Docker and release procedures must
explicitly generate before compiling; the build itself requires no Node.

Start with one classic IIFE bundle per page kind, content-hashed and
selected through a manifest. Vendor files and the synchronous `arrival`
script stay separate. Shared chunks and dynamic import wait for a measured
need; no whole-graph hash prefix is adopted now.

### Type checking and Node

Use strict, no-emit checking and erasable TypeScript only (no enums, runtime
namespaces or parameter properties). First choice: the stable native Go
checker in the portable `typescript@7.0.2` npm package verified by the spike,
without manually managing per-platform binaries. Check editor compatibility
too. A demonstrated migration compatibility problem permits classic
`typescript@6.0.3` as the fallback, recorded with the failing case; the two
checkers are not parallel dependencies.

Node 26.8.1 is the initial baseline, pinned reproducibly across CI, the
implementer's sandbox and local development by the details page's single
version file and update procedure. Node/npm are limited to type checking,
unit tests and dependency management, never the asset build or run time.

### Tests and lint

Test DOM-free `.ts` logic with Node's test runner. Start with `.mjs`
harnesses and no `@types/node`; reconsider only when a real need is shown.
Type checking and unit tests are separate required checks: Node strips
types without checking them. Keep browser behaviour tests.

No ESLint, Biome or oxlint initially. Use strict TypeScript and improve the
existing Go checks. If a concrete missing rule appears, compare that rule
and candidate tools in a separate issue. The Go check is no security boundary.

### DOM contract

Go is the source of truth for DOM ids, attribute names and event contracts;
generate their TypeScript representation. Generated names alone are not
enough: typed accessors and a Go check must reject stray contract string
literals outside the adapter. Client-specific state belongs in typed
in-memory modules, not `dataset`; server attributes and necessary derived
DOM projections remain adapters. Composer migration must preserve stream
reset notifications and immutable `draftKey` identity across request-key
rotation. Details specify generation, usage and acceptance.

### Source maps and CSP

Provide development source maps. Production maps are external, private
CI/release artifacts with the retention, access and output association in
the details page; never public, inlined or in the deployment image.

Keep the nonce-only CSP and verify the initial classic bundle in a browser.
Before ES-module delivery, shared chunks, lazy loading or `modulepreload`,
verify behaviour in the target browsers from the target-browsers document
(#1406). Specification reasoning in the spike is not browser verification;
never loosen CSP without verification.

### Existing checks

Adapt `web/static/javascript_test.go` for TypeScript sources, ES-module
scope and the hashed manifest. Preserve the intent of forbidden-API,
storage, request, DOM-manipulation and global-write checks; keep browser
tests. Regex patterns cannot establish semantics or detect every indirect
use, even with types; review and behaviour tests still cover those limits.

### Migration and acceptance

Keep #1404's staged foundation and stateful-first conversions after M7. Before any
implementation issue is made `ready`, set its measurement method and
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
  replace only its state rule with these two parts: never own the message
  list, sidebar or counts, subject to decision 34's existing exception;
  client-specific state lives in typed in-memory modules, not `dataset`.
  Decisions 34 and 37 stay unchanged. The no-requests rule is not amended.
- [Decision 35](35-tooling-in-go.md): add exactly one exception, the pinned
  Node type checker and unit-test runner, including their dependency
  management, only for the uses above. The builder, contract generator and
  all check orchestration remain Go under `tools/`; one-line Make recipes stay.
- [Decision 40](40-plain-javascript-by-default-library-islands-by-measured-exception.md):
  TypeScript replaces plain JavaScript as the default source language.
  Keep the island gate and per-feature route (§3), HTML rule (§4) and size
  budget/report (§5). Erasable types add no runtime code and are not a
  library under that gate; this adopts no UI library.

The maintainer approves TypeScript, Node for type checking, tests and
dependency management only, and the Go-driven esbuild build under this
decision, in the terms of decision 42 (#1437), reserved by draft PR #1441.
The language list and enforcement table must change with the toolchain
pull request so code and table agree. Any needed exception records one
exact path, issue and approving maintainer comment URL per line; this
record changes neither the table nor the exceptions list.

## Boundary with #1361

This decides no client-side cache, optimistic state, upload, request made
by a script on its own, Web Push or service worker. All twelve toolchain
rules above assume nothing about #1361's outcome. That later decision can
build on typed modules holding client state, generated contracts, the
build, tests and measurement gates without gaining any UX permission here.
The amendment to 19 is limited to the two state parts above, leaving #1361
free to amend 19, 30, 37 (c) and 41 afterwards without conflict.

## Built later by

- #1410: type checking, pins, safe install and npm dependency updates;
  #1411: Go builder, hashed delivery, manifest/embed and map handling.
- #1412: unit tests and source-check adaptation; #1413: generated DOM
  contract; #1414: keep new code typed; #1415: Node provisioning and AI workflow.
- #1409: conventions; #1416–#1421: stateful-first conversions;
  #1426: whole-milestone review, measurements and browser verification.

## Trade-offs

A bare `go build` from a fresh clone no longer works directly after the
build migration: generation must precede embedding. Development and CI
gain Node and an npm package with platform dependencies, although build
and deployment need no Node. The native checker's newness and editor
compatibility remain integration risks. Regex checks remain incomplete;
size evidence does not establish browser responsiveness.

**Why:** the existing composer already coordinates state, retries, drafts
and reset across scripts. Typed boundaries and logic tests address the
maintainer's priority of safely growing that code. The spike supports a
Go-only asset build and classic page bundles without justifying a UI library.

**Considered:** JSDoc/no-build (measured baseline, more verbose typed state
and no bundling savings); classic checker first (fallback retained, native
worked in the spike); committed output (fresh-clone convenience at the cost
of generated review diffs); per-platform native packages (manual platform
management); shared chunks and more lint tools (deferred until evidence).
