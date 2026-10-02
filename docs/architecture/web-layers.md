# Web layers: handlers, templ, htmx and JavaScript

What each browser-facing layer is responsible for, decided by the
maintainer on 2026-09-30 ([decision 19](../decisions/19-web-layers-server-owned-html-htmx-swaps-javascript-as-enhancement.md)). Follow
it for every page, fragment and script; M3's SSE work builds on it.

## Progressive enhancement

Core functionality works without JavaScript. JavaScript is progressive
enhancement, not full parity.

- **Core, today:** first-run setup, sign-up, sign-in and sign-out, opening
  a channel or topic and its history (including older pages), posting a message,
  branching and creating channels. Plain forms and links work alone;
  htmx and scripts improve them.
- **Enhancement only:** real-time updates, focus and scroll handling,
  keyboard shortcuts (Enter to send), and local-time display. Without
  JavaScript a page may lack these, but nothing core breaks.

Topic views use plain composer submissions.

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
  full page with `hx-select` is allowed; Load older does this today.
  Enhanced posts return only the `MessageComposer` fragment. On success its
  `data-posted-message` carries the posted message's DOM id; errors omit it.
  The stream script consumes that id to scroll once the item is present,
  regardless of response/delivery order or a reconnect.
- A unit that M3's SSE needs becomes an explicit templ fragment or
  component, rendered by the same code as the full page. `MessageItem`
  takes a `view.Message` and renders one `<li>` on its own or in the page.
- **DOM ids are a contract.** Templates define every id that `hx-target`,
  `hx-select`, `hx-select-oob` or a script refers to. Renaming one means
  updating every reference in the same pull request. Today's contract ids
  are `conversation`, `message-list`, `message-items`, `load-older`,
  `message-body`, `message-composer`, `message-help`, `message-status`,
  `branch-form`, `branch-to`, `branch-name` and `branch-feedback`, plus
  `message-<hex>` from `MessageDOMID` and `select-message-<hex>` for its
  checkbox (32 lowercase hex digits). `#message-items` is always present,
  including when empty; Load older selects its direct `<li>` children.
- The latest channel or topic page carries `data-event-cursor` on its outer
  layout div, outside `#conversation` and every swap target, with
  `hx-ext="sse"` and `sse-connect="<page URL>/events?after=<cursor>"`; a
  topic's stream sends only that topic's messages (#304). Older pages omit
  all three.
  `#message-items` receives `message` events; same-id duplicates replace the
  existing item. `LiveMessageItem` shares `MessageItem` markup, adding
  templ-rendered `data-announcement` text only to stream payloads; the
  render cache stores those live items, attribute included. History pages,
  including Load older, omit it.
  `#message-status` retains only the latest 10 announcements; history itself
  is never a live region.
- The layout's `htmx-config` meta tag holds htmx's security settings and
  `responseHandling` ([`rendering.md`](rendering.md)). It keeps the three
  defaults, adding global 409/422 swaps (`error: false`) before `[45]..`.
  Composer errors replace `#message-composer`; branch errors replace only
  `#branch-feedback`, so selections and drafts survive.

## JavaScript

JavaScript never generates HTML and never owns application state. It is
limited to UX help that HTML and htmx handle poorly: focus, scroll,
keyboard, local time, single-source selection constraints, and glue for SSE.

- SSE `reset` closes htmx's event source and reloads the page to obtain a fresh snapshot.
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
  keep their own globals and upstream file names ([a11y]).
- **htmx events:** an `htmx:load` handler works only on the inserted element
  and its descendants, never the whole document (#168).

A headless-browser test dependency (for example go-rod) is not used now;
it is decided after M3, only if a real need appears.

## How the rules are checked

Checked by tools today: the layer imports (depguard), no `templ.Raw`
(forbidigo), and the markup rules in `TestPagesMarkup` ([a11y]).
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

`TestComponentsMarkup` renders `SignOutButton`, `MemberName`, `MessageItem`
and `LiveMessageItem` alone in both languages and applies the fragment
markup rules.
`TestPagesMarkup` also checks the composer fragments returned by handlers.

`TestPagesMarkup` also checks every `hx-get`/`hx-post` element for a plain
link or form with the same URL and HTTP method (`fallback_test.go`). URL
comparison is structural; paging success uses positive cursors in
`TestMessagePagingHandler`.

`TestMessagePostHandler` covers ordinary/HX success (303/200) and validation
errors (422). `TestMessagePagingHandler` covers Load older with and without
HX (200 full pages). `TestChannelHandlers` verifies that HX changes nothing
for unenhanced channel creation (303/422). Plain posting errors and history
responses assert the full layout; enhanced posting responses assert the
composer alone, without another history read.

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

## Audit, 2026-10-01 (M3)

Every HTML handler in `internal/web`, every templ component and every
application script in `web/static` was read against these rules, first on
2026-09-30 and again after M3 (#163).

| Where | Finding | Fixed by |
|---|---|---|
| `view/channel.templ`: `ChannelPage.Messages` | The handler now converts app entries to `view.Message` values | #194; depguard `view` rule |
| Composer script's `htmx:beforeSwap` made 422 responses swap | JavaScript decided what htmx swaps | #198: layout's htmx config |
| `message-stream-v2.js` swaps through `htmx.swap`: `outerHTML` for a message id already present, else append | The extension's swap style is fixed per element, so it cannot replace by id or append | Kept, by design: SSE glue; the HTML stays server-rendered (#159) |

Everything else conforms:

- **Handlers.** Pages, the composer fragment and the event stream render
  view models through templ.
- **Templates.** No I/O or authorisation; the announcement is escaped.
- **Core flows.** Plain forms and links work without JavaScript.
- **Scripts.** Focus, scroll, keyboard, local time and SSE glue; no requests,
  storage or globals.

[a11y]: ../accessibility.md
