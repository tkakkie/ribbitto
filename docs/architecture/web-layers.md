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

Handlers in `internal/web` validate requests, call use cases, convert results
to view models, and choose the status, component and full page or fragment. They never build HTML strings
(`AGENTS.md`); plain-text responses such as `/healthz` are not HTML.

## templ

Components render view models defined in `internal/web/view`. They do not
receive `internal/app` or use-case types: handlers convert those. A view
model may hold pure `domain` value types (`domain.Channel` and `domain.ID`).
`ChannelPage.Organization` is a view-owned `Organization` with `Slug` and
`Name`; handlers convert from `org.Organization`. Views do not import `org`. Components do no I/O and make no authorisation decisions;
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
- **The data attributes that scripts read are a contract too,** with the
  same rule for renaming (#351). A missing one fails silently: without
  `data-oldest-seq`, the move handler throws after cancelling the SSE event,
  so the move is neither applied nor swapped. Besides `data-event-cursor`,
  `data-posted-message` and `data-announcement` (described in this section):

  | Attribute | On | Present | Meaning |
  |---|---|---|---|
  | `data-oldest-seq` | `#load-older` | always | `event_seq` of the oldest loaded message, `0` once nothing older remains; a moved item is inserted only at or above it |
  | `data-topic` | `#message-items` | topic pages only | the topic's ID, compared with a move's topics |
  | `data-from-topic`, `data-to-topic` | a `messages-moved` payload's `<ul>` | always | the move's source and destination topic IDs |
  | `data-event-seq` | each message `<li>` | always | its `event_seq`, which orders an inserted item |
  | `data-source` | each message's checkbox | always | its topic's ID; a selection stays within one source topic |
- The latest channel/topic page carries `data-event-cursor` on its outer
  layout div, outside `#conversation` and every swap target, with
  `hx-ext="sse"` and `sse-connect="<page URL>/events?after=<cursor>"`; a
  topic's stream sends only that topic's messages (#304). Older pages omit
  all three.
  `#message-items` receives `message` and `messages-moved` events; moves
  replace feed IDs or remove/insert topic IDs within the loaded range. Replacements clear selection and refresh
  branch constraints. `LiveMessageItem` shares `MessageItem` markup, adding
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

Move/history regression tests use an installed headless browser through
go-rod (#349); see the [checks](web-layers-checks.md).

## How the rules are checked

The checks, the JavaScript pattern check and the M3 audit are in
[`web-layers-checks.md`](web-layers-checks.md).

[a11y]: ../accessibility.md
