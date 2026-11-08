# Web toolchain

[Decision 43](../decisions/43-typescript-with-a-go-driven-build.md) sets the rules;
decision 45 sets `.test.ts` harnesses.

## Language approval — #1410–#1413

Under [decision 42](../decisions/42-repository-languages-and-formats.md), the
maintainer's [approval record](https://github.com/tkakkie/ribbitto/issues/1356#issuecomment-6289973261)
covers TypeScript, Node only for type checking/tests/dependency management, and
the Go-driven esbuild build.

Approved locations: web/src/*.ts (including declarations), root tsconfig.json,
package.json/package-lock.json and .node-version; `web/src/package.json`
(`{"type": "module"}`); direct web/test/*.test.ts, node-test.d.ts and package.json.
Go builders under tools/ are covered. Add rows to decision 42 and
[`tools/languagecheck`](../../tools/languagecheck/rules.go) with the first file
of a new kind or location. Unlisted files fail; exact-path exceptions use
[`docs/language-exceptions.txt`](../language-exceptions.txt).

Review checks Makefile/workflow node/npm commands against that limited approval
(decision 42 leaves commands to review); languagecheck does not scan them and they
need no table entries.

## Build, manifest and embedding — #1411

**Naming — confirmed:** `make bundles` runs `tools/webbuild` via esbuild's Go API, pinned only in
the tools module. Each named `web/src/<name>.ts` entry becomes a minified
classic IIFE named by esbuild's `[name]-[hash].js`, without shared chunks or
dynamic imports. ES2018 avoids needless object spread/rest helpers in supported
browsers ([browser target rule 4](browser-targets.md#design-rules)).
This syntax floor adds no Web API polyfills and does not complete browser
verification; #1820 checks the served bundle.

Logical manifest entries record bundle filenames and SHA-256 content hashes;
commit SHA and run id are build inputs. Production output has no maps or
source-map links.

`web/static/scripts.go` owns the shared page-entry list; non-entry `web/src/`
modules are never bundled standalone. Validate the manifest and bundle hashes
at startup; missing/unknown entries or bundles and undeclared renders fail.
Use no mutable globals. Type checking covers authored TypeScript, excluding
SHA-256-pinned `web/static/vendor` and generated `web/static/dist` scripts.
`web/legacy-scripts.txt` is empty.
Classic bundle: deferred, with nonce; arrival stays synchronous.
Converted bundles are strict-mode scripts (assignments to non-writable properties throw, which sloppy legacy scripts silently ignored).
Registered hashed `/static/dist/` bundles are immutable; the manifest is private.

Fresh checkout: `make bundles` before direct `go build ./...`, `go test`,
`go vet`, `go run` or tools loading the tree. Make build/check/run, CI schema
verification, testshard and CodeQL generate first. Embed patterns require
generation; ignored `dist` is never committed. CI checks generation/embedding/build
without Node on `PATH`. Future Docker/release workflows must generate before
compiling and package that output (none exists).
Host builds use the
[writable-copy routes](../workflow/investigations.md#build-routes).

Development bundles and the `RIBBITTO_DEV_ASSETS` serving rules are in
[source maps](web-source-maps.md#source-maps--1411).

## Versions and installation — #1410, #1415

**Node pin mechanics — confirmed:** one root `.node-version` containing `26.8.1`. CI and local
development read this same pin; Go wrappers used by one-line
Make recipes reject a different runtime version. Use that Node release's
bundled npm, not a separately upgraded global npm. The wrapper resolves npm
beside the version-checked Node without resolving symlinks. CI provisions the release
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
recorded migration/checker/editor incompatibility.

Run `make web-install` explicitly, then `make check-web`; checks never install.
`make check` and `make prepush` print `NOT VERIFIED` and skip when pinned Node
or the checker is missing/mismatched, unless `RIBBITTO_REQUIRE_NODE=1` makes
that fail. CI's `web-types` job installs and checks with that flag.
Checker/runner probes and hermetic wrapper tests are described in
[checks](web-toolchain-checks.md#checks-and-measurement--1412-1414-1426). The strict,
no-emit config includes web/src/**/*.ts (web/test/*.test.ts, node-test.d.ts from #2092); the empty
`web/legacy-scripts.txt` rejects later additions (#1414). `globals.d.ts` supplies the first input and the htmx global.

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
path aliases. Client unit tests and `make test-web` follow
[`web-tests.md`](web-tests.md) and [checks](web-toolchain-checks.md).

## Generated contract and state — #1413, #1416, #1417

The [generated contract](web-contracts-generated.md) defines ownership,
accessors, runtime validation and literal checks.

[Decision 43](../decisions/43-typescript-with-a-go-driven-build.md) sets the
allowed typed working state, persistence and permissions.
#1416/#1417 must
coordinate composer and stream
reset guards: changing queue state must notify/reset the guard even without
DOM mutations. Keep immutable `draftKey` tied to the original draft snapshot,
separate from a 409-rotated retry request key; preserve
stream-first/response-first settlement. Browser tests retain these guarantees,
with contract-specific fixture updates where needed, never weakened
assertions.

See [source maps](web-source-maps.md#source-maps--1411).

See [checks and measurement](web-toolchain-checks.md#checks-and-measurement--1412-1414-1426).
