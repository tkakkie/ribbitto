# Web layers: handlers, templ, htmx and JavaScript

What each browser-facing layer is responsible for, decided by the
maintainer on 2026-09-30 ([`DECISIONS.md`](../../DECISIONS.md), 19). Follow
it for every page, fragment and script; M3's SSE work builds on it.

## Progressive enhancement

Core functionality works without JavaScript. JavaScript is progressive
enhancement, not full parity.

- **Core, today:** first-run setup, sign-up, sign-in and sign-out, opening
  a channel and its history (including older pages), posting a message,
  and creating a channel. Each is a plain HTML form or link that works on
  its own; htmx and scripts only improve it.
- **Enhancement only:** real-time updates, focus and scroll handling,
  keyboard shortcuts (Enter to send), and local-time display. Without
  JavaScript a page may lack these, but nothing core breaks.

## Go handlers

Handlers in `internal/web` parse and validate the request, call the use
case, convert its result into a view model, and choose the status, the
component, and a full page or a fragment. They never build HTML strings
(`AGENTS.md`); plain-text responses such as `/healthz` are not HTML.

## templ

Components render view models defined in `internal/web/view`. They do not
receive `internal/app` or use-case types: handlers convert those. A view
model may hold pure `domain` value types (such as `domain.Organization`,
`domain.Channel` and `domain.ID`), which carry no behaviour a template
could misuse. Components do no I/O and make no authorisation decisions;
presentation logic (building URLs, formatting, choosing an i18n message
ID) belongs in them.

## htmx

htmx does only requests and swaps; the server and templ own the HTML.

- A normal request gets a full page. Selecting part of a server-rendered
  full page with `hx-select` is allowed; the composer and Load older do this
  today.
- A unit that M3's SSE needs becomes an explicit templ fragment or
  component, rendered by the same code as the full page (#154 M1: a
  `MessageItem` fragment and a DOM-id helper).
- **DOM ids are a contract.** Templates define every id that `hx-target`,
  `hx-select`, `hx-select-oob` or a script refers to. Renaming one means
  updating every reference in the same pull request. Today's contract ids
  are `conversation`, `message-list`, `message-items`, `load-older` and
  `message-body`.
- The layout's `htmx-config` meta tag holds htmx's security settings and
  `responseHandling` ([`rendering.md`](rendering.md)). It keeps the three
  defaults, inserting a 422 swap with `error: false` before `[45]..` because
  the first matching entry wins. This rule is global: a new 422-returning
  htmx request inherits it. Today only the composer's `#conversation`
  request can return 422; channel creation uses a plain form, and Load
  older retains default handling for success and errors.

## JavaScript

JavaScript never generates HTML and never owns application state. It is
limited to UX help that HTML and htmx handle poorly: focus, scroll,
keyboard, local time, and glue for SSE.

- **No requests of its own:** no `fetch` or `XMLHttpRequest`. Requests go
  through HTML forms, links and htmx.
- **Storage:** `localStorage` and `sessionStorage` may hold only
  non-sensitive UX settings whose loss affects no function and no server
  state, such as whether a panel is open or a display preference. They
  never hold messages, unsent drafts, credentials, session tokens or other
  server-derived application data. IndexedDB is not used; a clear need,
  such as offline support, gets its own issue.
- **Files:** every script is external in `web/static` and loaded with the
  response nonce. Application scripts also define no globals and are
  versioned by file name (`-vN`), since static assets are immutable
  ([`rendering.md`](rendering.md)). Vendored libraries (htmx, idiomorph)
  keep their own globals and upstream file names ([`ui.md`](../ui.md)).
- **htmx events:** an `htmx:load` handler works only on the inserted element
  and its descendants, never the whole document (#168).

A headless-browser test dependency (for example go-rod) is not used now;
it is decided after M3, only if a real need appears.

## How the rules are checked

Checked by tools today: the layer imports (depguard), no `templ.Raw`
(forbidigo), and the markup rules in `TestPagesMarkup` ([`ui.md`](../ui.md)).
The depguard `view` rule forbids non-test files in `internal/web/view`
from importing `internal/app` or `internal/infra`, including sub-packages.
`TestPagesMarkup` also follows every `hx-get` and `hx-post` in each case,
in both languages, with `HX-Request: true`. It checks `hx-target` ids in
the requesting page, `hx-select` matches in the response, and every
`hx-select-oob` match in the response and its destination id in the page.
Selection may use response-only ids. The selector helper supports `#id`
and `#id > tag`, including comma-separated lists; new syntax needs explicit
test support. Non-id targets `this`, `closest …` and `find …` are allowed;
any ids they name are still checked in the page. The composer's
`hx-disabled-elt="find textarea, find button"` and `hx-sync="this:drop"`
are not swap selectors.

`TestComponentsMarkup` renders `SignOutButton` and `MemberName` alone in
both languages and applies the fragment markup rules. Handler and SSE
fragments join that list when M3 adds them.

Planned:

| Rule | Issue |
|---|---|
| Enhanced forms and links work without `HX-Request`, and HX responses keep their contracts | #196 |
| Application scripts: no HTML writes, requests, IndexedDB, evaluation or globals; versioned names | #197 |

Left to review, because a pattern check cannot prove them: that a stored
value is only a UX setting, that a script owns no application state, and
that a component makes no authorisation decision.

### JavaScript pattern check

`TestApplicationJavaScript` in `web/static/javascript_test.go` checks every
direct `web/static/*.js` file, regardless of its name or version; it does
not descend into subdirectories such as `vendor/`. Names must end in
`-vN.js`, where N is one or more decimal digits. This implements #197.

The check matches source patterns on each line after masking `//` and
`/* ... */` comments and single-, double- and backtick-quoted contents
(including escapes). Quote delimiters remain visible for string timers.
It rejects:

- `innerHTML`/`outerHTML` assignments, including compound assignments such
  as `+=`; calls to `insertAdjacentHTML`, `document.write`,
  `document.writeln` and `createContextualFragment`. Reads and comparisons
  of HTML properties pass.
- `fetch`, `XMLHttpRequest` and `indexedDB` identifiers.
- Calls to `eval`, `new Function`, and `setTimeout`/`setInterval` with a
  directly quoted first argument. These evaluation restrictions restate
  the existing CSP without `'unsafe-eval'` ([`rendering.md`](rendering.md)).
- `var`/`let`/`const` and named `function` declarations at zero brace and
  parenthesis depth (a keyword after `.`, such as `options.var`, is a
  property name and passes), plus direct dot-property assignments and increments
  or decrements on `window`/`globalThis`. No particular file structure is
  required; multiple IIFEs and scoped event handlers pass.

This is a bounded pattern check, not a JavaScript parser. It does not
resolve aliases, computed properties, expressions split across lines or
template interpolations. Regular-expression literals are not recognized;
their contents can be mistaken for code, comments or delimiters. Delimiter
depth is not scope analysis: block-level `var`, implicit globals and other
indirect writes can escape it, while some named function expressions can
look like declarations. Reviewers check these cases, all HTML writes and
globals by hand. `localStorage` and `sessionStorage` pass mechanically; the
check cannot prove that a stored value is only a non-sensitive UX setting.

## Audit, 2026-09-30

Every HTML handler in `internal/web`, every templ component and every
application script in `web/static` was read against these rules.

| Where | Finding | Fixed by |
|---|---|---|
| `internal/web/view/channel.templ`: `ChannelPage.Messages` | Fixed: the handler converts app entries to `view.Message` values | #194; depguard `view` rule |
| Composer script's `htmx:beforeSwap` made 422 responses swap | JavaScript decided what htmx swaps | #198: moved into the layout's htmx config; listener removed in `message-composer-v2.js` |

Everything else conforms:

- **Handlers.** Setup, sign-up, sign-in, the home page, channels and
  posting all fill a view model and render through templ.
- **Templates.** They do no I/O and make no authorisation decisions.
- **Core flows.** Every core flow is a plain form or link.
- **Selection.** The composer and Load older select from full pages.
- **Scripts.** The scripts do keyboard, focus, scroll and local time only,
  with no requests and no storage.

The audit is repeated as part of M3's acceptance check (Status #1).
