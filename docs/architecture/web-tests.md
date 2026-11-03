# Client test layers and rules

Client-side test layers, what not to test, and the rules for writing,
converting, splitting, deleting and reviewing client tests under
[decision 43](../decisions/43-typescript-with-a-go-driven-build.md),
[decision 45](../decisions/45-type-checked-client-unit-tests.md) and milestone
M11 ([#2089](https://github.com/tkakkie/ribbitto/issues/2089); motivated by the
`409d680` survey recorded on
[#2](https://github.com/tkakkie/ribbitto/issues/2#issuecomment-6594314637)).

**When this page applies:** from the merge of #2092, every new or converted
client unit test is a type-checked `web/test/<name>.test.ts` file;
`web/test/*.test.mjs` files that still exist are only files that #2093–#2095 have
not yet converted (the `.mjs` allowance is removed in #2098). Tests are not kept for
their own count or length: test count and line total are not targets.

## Layers and what each owns

Each code issue's *Done when* names the layer that proves each item:

| Layer | Where and how it runs | What it owns |
|---|---|---|
| **Unit** | `web/test/<name>.test.ts`, type-checked by `make check-web` (`typescript@7.0.2`) and run by `make test-web` (`node --test` via [`tools/webtypes`](web-toolchain-checks.md)) | Pure, DOM-free decision logic in `web/src/<concern>.ts` and shared `web/src/<helper>.ts` modules: state transitions, queue order, retry classification, cursor advancement, parsing/validation and bounded storage rules using injected clock, random source, IDs and storage |
| **Glue** | Thin DOM and htmx entry modules in `web/src/*.ts` (and remaining legacy scripts in `web/static/*.js`) | Event listeners, DOM projections, focus, scroll, keyboard handling, local time formatting and SSE swaps; glue has no Node unit tests of its own and is proved through the browser layer |
| **Browser** | `TestBrowser` in `internal/web/message_stream_browser_test.go` and browser suites in `cmd/ribbitto` (headless Chrome via go-rod; [`web-layers-browser-tests.md`](web-layers-browser-tests.md)) | DOM and htmx integration, real event order, SSE delivery and swaps, focus/scroll/keyboard behaviour, script-disabled HTML/CSS fallbacks, and real browser timers and storage across navigation and swaps |
| **Go checks** | `TestApplicationJavaScript` in `web/static/javascript_test.go`, `tools/contractliterals`, `tools/typedscripts`, `tools/languagecheck`, `tools/webtypes`, `TestPagesMarkup` and `TestComponentsMarkup` ([`web-layers-checks.md`](web-layers-checks.md), [`web-toolchain-checks.md`](web-toolchain-checks.md)) | File names and locations, module and `tsconfig.json` shape, forbidden APIs and globals, generated DOM-contract literals (`web/src/contracts.gen.ts`), and server-rendered HTML/htmx markup contracts |

## When not to write a test

Do not write a client unit test for:

- **Glue:** DOM and htmx wiring (`arrival`, `side-panel`, `popover-focus`,
  `message-time`, `composer.ts` DOM wiring, event listeners, DOM reads/writes
  and swaps) belongs to `TestBrowser`, never to a Node harness.
- **Equivalence-class duplicates:** extra table rows that exercise the same
  branch and equivalence class with different literals (such as a dozen rows for
  a one-expression predicate) add reading cost without catching more bugs. Keep
  one row per equivalence class and boundary.
- **What types already guarantee:** strict TypeScript (`make check-web`) and
  generated contracts (`web/src/contracts.gen.ts`) already reject wrong
  parameter types, missing fields and unknown contract keys. Do not cast invalid
  TypeScript inputs (`as unknown as ...`) just to re-test the type checker;
  test runtime narrowing only at untrusted boundaries (server attributes, stored
  JSON, htmx event `detail`).
- **Call counts or order of clocks and helpers:** behaviour is what leaves the
  module and is observable outside it — returned values, state transitions, DOM
  writes, dispatched events, request parameters, stored values, timer delays,
  escaping exceptions and their order
  ([`reviewing.md`](../workflow/reviewing.md#pull-request-cross-review)) — not
  how many times an injected clock, storage getter or internal helper was read,
  nor internal call traces when the observable output is unchanged.
- **Degenerate-environment parity:** do not turn legacy quirks ("as `-vN` did")
  or degenerate-environment cases (such as an API that is present but throws in
  a way no browser or repository script produces, or an accepted parity
  difference) into permanent unit tests. Pre-review parity evidence stays in the
  pull request body.
- **Helpers no glue imports:** do not extract or keep a DOM-free helper (or its
  unit tests) that no production module in `web/src/` imports.

## Rules

- **Required tests per change:**
  - New or changed DOM-free decision logic in `web/src/<concern>.ts` (or a
    shared `web/src/<helper>.ts` extracted once a second module imports it;
    [`web-conventions.md`](web-conventions.md)) requires unit tests in
    `web/test/<name>.test.ts` covering its state transitions, boundaries and
    error paths.
  - New or changed DOM/htmx integration or user-visible browser flow requires a
    `TestBrowser` case when existing browser scenarios do not cover it.
  - Every pull request that adds or changes a client unit test records the
    deliberate-defect proof in *Verified by*: one deliberate defect in the
    production code under test that breaks the guarded behaviour and fails the
    test's assertion (not a crash or a compile/type error), and that the test
    passes again once reverted
    ([`reviewing.md`](../workflow/reviewing.md#pull-request-cross-review)).
- **One `test()` per rule with named table rows:** group each behavioural rule
  or invariant into one `test()` block with a table of named rows
  (`{ name, ... }`), iterating over the table and passing `row.name` as the
  assertion message so a failure identifies the broken case. Do not split one
  invariant into many single-case `test()` calls, and include only distinct
  equivalence classes and boundaries.
- **Injected clock, random source, IDs and storage (no patched globals, no fake
  DOM):** pass clocks (`now`), random sources, generated IDs and storage
  adapters (`getItem`/`setItem`/`removeItem` or a narrow interface) as explicit
  parameters or options to the DOM-free module. Never patch or mutate
  `globalThis`, `window`, `document`, `Date.now`, `Math.random` or `crypto`, and
  never construct a fake `window`, `document`, `Element` or `CustomEvent` (or
  add a DOM emulator such as `jsdom` or `happy-dom`) in `web/test/`.
- **File naming, imports and checks:** unit test harnesses live directly in
  `web/test/<name>.test.ts` (flat; no subdirectories or helper `.ts` files in
  `web/test/`), named for the `web/src/<name>.ts` module they test. Harnesses
  import DOM-free `web/src/<concern>.ts` modules by relative `.ts` path
  (`../src/<concern>.ts`) and import `node:test` and `node:assert/strict` typed
  by the hand-written `web/test/node-test.d.ts` declaration file (no
  `@types/node`). Stripping types in `node --test` (`make test-web`) does not
  type-check them, so both `make check-web` and `make test-web` remain required
  ([`web-toolchain-checks.md`](web-toolchain-checks.md)).
- **Splitting a harness that grows past one concern:** each harness covers one
  concern. When a harness grows past one concern (for example, combining
  storage encoding/quota rules with session-expiry or UI orchestration), split
  it into focused `web/test/<concern>-<part>.test.ts` harnesses (or alongside
  split `web/src/` modules). This is guidance for authors and review, not a
  numeric line or test-count limit.
- **Conversion parity stays in the pull request body:** on a
  behaviour-preserving conversion of a legacy `web/static/<name>-vN.js` script
  to `web/src/<name>.ts`, the differential comparison against the baseline
  script (baseline SHA, commands and result, including clock, storage failures
  and malformed values) is recorded in the pull request body
  ([`delivery.md`](../workflow/delivery.md#standard-implementer-brief),
  [`reviewing.md`](../workflow/reviewing.md#pull-request-cross-review)), not
  kept as a permanent file or permanent "as `-vN` did" unit tests.
- **Deleting or merging tests:**
  - Removing or merging a test that guarded behaviour must name in the pull
    request the place (`web/test/*.test.ts` case, `TestBrowser` scenario or Go
    check) that still covers that behaviour.
  - A test that checked only an implementation detail (such as call counts or
    order of clocks and helpers), a conversion-parity quirk, or an
    equivalence-class duplicate may be removed with the reason stated in the
    pull request.
  - No replacement test is written to keep a test count or line total.

## Cross-reviewer checks

Alongside [`reviewing.md`](../workflow/reviewing.md#pull-request-cross-review):

- **`[blocking]`:** a missing required test for new or changed behaviour;
  global patching (`globalThis`, `window`, `document`, `Date`, `Math`, `crypto`)
  or DOM fakes in `web/test/`; removing or merging a behaviour-guarding test
  without naming what still covers it; or a missing deliberate-defect proof in
  *Verified by* when a pull request adds or changes a client unit test.
- **`[nit]`:** redundant table rows within an already-covered equivalence class.
