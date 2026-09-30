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
- Today the layout's `htmx-config` meta tag holds htmx's security
  settings ([`rendering.md`](rendering.md)). The composer's 422 swap rule is
  still decided in JavaScript, a deviation that #198 moves into that
  config.

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
Planned:

| Rule | Issue |
|---|---|
| `hx-target`, `hx-select` and `hx-select-oob` resolve; shared components pass the fragment rules | #195 |
| Enhanced forms and links work without `HX-Request`, and HX responses keep their contracts | #196 |
| Application scripts: no HTML writes, requests, IndexedDB, evaluation or globals; versioned names | #197 |

Left to review, because a pattern check cannot prove them: that a stored
value is only a UX setting, that a script owns no application state, and
that a component makes no authorisation decision.

## Audit, 2026-09-30

Every HTML handler in `internal/web`, every templ component and every
application script in `web/static` was read against these rules.

| Where | Finding | Fixed by |
|---|---|---|
| `internal/web/view/channel.templ`: `ChannelPage.Messages` | Fixed: the handler converts app entries to `view.Message` values | #194; depguard `view` rule |
| `web/static/message-composer-v1.js`: `htmx:beforeSwap` makes 422 responses swap | JavaScript decides what htmx swaps | #198 |

Everything else conforms:

- **Handlers.** Setup, sign-up, sign-in, the home page, channels and
  posting all fill a view model and render through templ.
- **Templates.** They do no I/O and make no authorisation decisions.
- **Core flows.** Every core flow is a plain form or link.
- **Selection.** The composer and Load older select from full pages.
- **Scripts.** The scripts do keyboard, focus, scroll and local time only,
  with no requests and no storage.

The audit is repeated as part of M3's acceptance check (Status #1).
