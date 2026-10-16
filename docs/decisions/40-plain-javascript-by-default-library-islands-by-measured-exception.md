# 40. Front-end JavaScript: plain by default, UI-library islands by measured exception

**Decided** (#1245, option B, approved by the maintainer on 2026-10-17). It
amends [decision 2](02-server-rendered-html-with-htmx-not-an-spa.md) (no
npm, no JavaScript build step) and
[decision 19](19-web-layers-server-owned-html-htmx-swaps-javascript-as-enhancement.md)
(JavaScript generates no HTML and owns no application state) with a route
for scoped exceptions. It adopts no library and changes no build or
tooling. The goal is a fast, comfortable chat that is easy to extend, not
the least JavaScript: plain JavaScript by default, with room to adopt
Preact, Svelte, Lit or a dedicated library when a feature needs one.
Neither keeping the early constraints nor adopting libraries is a goal in
itself: start light, and re-evaluate when a feature actually needs more.

## 1. Default

Server-rendered HTML with htmx, and plain JavaScript as enhancement.
Decisions 2, 19, 34 and 37 apply unchanged to plain scripts.

## 2. The M7 composer

Mention candidates, quoting, drafts, the send key and `chat_v1` input help
stay plain JavaScript with htmx:

- plain JavaScript modules, one concern per script;
- a shared helper only once duplication appears;
- no reactive library up front; `@preact/signals-core` is considered only
  when composer state is clearly complex.

Each feature's pull request reports its head-minus-base raw and `gzip -9`
(stdin) bytes and the composer's cumulative total. These replace an
estimate of about 9–23 KB gzip, unminified, for all planned M7 features
(judged from comparable existing scripts, not measured; #1245).

## 3. When a library may be introduced

All of these, stated in the feature's own issue:

- **Need:** the feature renders client data into substantial DOM, or holds
  interactive state that server fragments over htmx serve poorly: filtering
  a large emoji grid, a rich-text editing surface, multi-step input
  assistance. A wish for a framework alone is not a need.
- **Candidates compared** for that feature: plain JavaScript (with small
  shared helpers); Preact; Svelte; Lit; a dedicated, framework-agnostic
  library for the job (an emoji-picker component, an editor engine).
- **Criteria:** delivered size (raw and gzip, the whole page and the added
  part); runtime performance; UX; maintainability; security; fit with
  server rendering; CSP as it stands (no `'unsafe-inline'`, no
  `'unsafe-eval'`, no injected styles; `internal/web/middleware/security.go`);
  no HTML from strings (point 4); IME behaviour; coexistence with htmx swaps
  (mount, and unmount on cleanup); accessibility; licence, maintenance and
  advisories; toolchain cost (a vendored prebuilt file, like htmx, versus
  npm and Node).
- **Measured under #1152's conditions:** #1149's harness, base and head
  alternating in one session, the same profiles and the quiet-machine rule.
  Input latency, INP, own post where affected, initial JavaScript and memory
  are measured, not only size. A library is measured when a feature needs
  it, not in advance.
- **Limits fixed before measuring:** the adopting issue states its
  numerical limits and methods, and `ready` approves them together with the
  measurement work and its cap
  ([investigations](../workflow/investigations.md)). Limits never change
  after measuring starts.
- **Two separate steps:** sizing a library's minimal sample, which sets its
  allowance (point 5); then comparing the candidates for the feature itself,
  which decides the adoption.
- **Scope:** the island owns only its own component's DOM; the message
  list, sidebar, counts and presence stay server-rendered. Requests and
  storage follow decision 37 and the *Storage* rules in
  [`web-javascript.md`](../architecture/web-javascript.md), and any later
  decision that changes them (client storage:
  [decision 41](41-client-storage-indexeddb-by-data-class.md)) applies to
  islands as to plain scripts. Each island is a scoped exception to decision
  19, listed there like decisions 34 and 37.
- **Toolchain:** a vendored file needs no exception. npm and a build step
  need the adopting decision to name them as an exception to decision 2 and
  [decision 35](35-tooling-in-go.md). For Svelte that is #1172, #1194,
  #1187 and then #1166, which stay open until a feature needs them.
- **Checks:** an adopted library gets authored-code checks before
  production code, as #1166 sets out for Svelte.
- **Approval:** the exception names the feature, its need, its safeguards
  and the alternatives, and the maintainer approves it individually.

## 4. HTML from strings

The rule stays: authored code never builds HTML from strings. It
distinguishes two things:

- **Interpreting an arbitrary string as HTML or SVG structure** stays
  banned in principle, in any library: Lit's `unsafeHTML`, `unsafeSVG` and
  `unsafeStatic`, and their equivalents elsewhere.
- **Building DOM from static templates**, as Lit's tagged templates do
  (markup fixed in the source, values bound only as text, attributes or
  properties), is not that, and may be allowed for an adopted library
  under point 3, with its checks.

Lit is a future exception candidate, neither adopted nor granted an
exception now. It is considered when a feature becomes too complex for
plain JavaScript or small shared helpers, and decided case by case under
point 3.

## 5. Performance budget

This replaces a single flat added-JavaScript cap for future features.
#1152's approved budget is not changed after the fact.

- **Whole page:** every feature's pull request reports a channel page's
  total delivered JavaScript (raw and gzip) and its change.
- **Per feature:** head minus base, gzip as served, in each feature's pull
  request. **3 KB (3,000 bytes) is a provisional target, not a hard cap.**
  A feature may exceed it after its need, its measured values, its UX
  benefit and its maintainability are checked; the exception's conditions
  and measurement criteria are approved before it is implemented and
  measured. Features and files are never split unnaturally just to meet
  the number.
- **Initial load and lazy load are budgeted separately.** Payloads loaded on
  demand (emoji data, an editor engine) have their own budget, outside
  initial load.
- **Library allowance:** one-time, set by the adopting decision from a
  measured minimal sample, and revisited when later features use more of the
  library.
- **Not size alone:** input latency, INP, memory and how the app feels in
  use are weighed with size. Input latency, INP and own post stay gates
  wherever a feature touches them; size alone never outweighs measured
  responsiveness or maintainability.

## #1152's outcome

#1152 closes with this decision. It measured size only: its Svelte composer
island added **+19,897 bytes gzip** to a channel page
([sizes](https://github.com/tkakkie/ribbitto/issues/1152#issuecomment-6218490135)).
No other performance item (input latency, INP, own post, initial
JavaScript, memory) was evaluated, and there is no overall pass/fail
verdict
([measured and unmeasured items](https://github.com/tkakkie/ribbitto/issues/1152#issuecomment-6218936594)).
Svelte is not rejected: it is not adopted for the current composer, and is
compared and measured again when a feature needs it. The prototype branch
stays unmerged.

**Why:** M7's composer features are small enhancements that htmx and plain
scripts serve, so they pay no toolchain or runtime. Features where client
state and rendering are natural (an emoji picker, advanced mention
assistance, rich-text editing) get a known path instead of a fresh policy
debate, and their library is chosen for their needs. The server-rendered
core and today's checks stay.

**Considered:**

- **A. Keep decisions 2 and 19 as they are**, with case-by-case
  exceptions: no change, but each feature repeats the policy work (when a
  library is allowed, what it may own, how it is measured).
- **C. Adopt Svelte now** and go ahead with #1172 → #1194 → #1187 → #1166:
  runes and a compiled template made the prototype short to write (an
  authoring impression, not a measurement), but it pays the toolchain and
  runtime before any feature needs them, and M7 does not.

First data for point 3's sizing step, exploratory and not equivalent
composer implementations: one textarea with a reactive `aria-invalid`, two
handlers and mount/unmount, minified with esbuild, in gzip bytes — plain DOM
219, `@preact/signals-core` 1,816, Preact with hooks 5,453, Lit 5,790,
Solid 7,685, Preact with `@preact/signals` 7,956, Svelte 17,496
([samples and build](https://github.com/tkakkie/ribbitto/issues/1245#issuecomment-6219079003)).
