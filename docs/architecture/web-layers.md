# Web layers: handlers, templ, htmx and JavaScript

What each browser-facing layer is responsible for, decided by the
maintainer on 2026-09-30 ([decision 19](../decisions/19-web-layers-server-owned-html-htmx-swaps-javascript-as-enhancement.md)). Follow
it for every page, fragment and script; M3's SSE work builds on it.

## Progressive enhancement

Core functionality works without JavaScript. JavaScript is progressive
enhancement, not full parity.

- **Core, today:** first-run setup, sign-up, sign-in and sign-out, opening
  a channel, its members page or a topic and its history (including older pages), posting a message,
  branching, marking a feed or topic read, and creating channels. Plain forms and links work alone;
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
receive module or use-case types: handlers convert those. View
models own their `Organization` (`Slug`, `Name`), `Channel` (`ID`, `Name`)
and `Topic` (`ID`, `Name`, `IsDefault`); handlers convert from `org` and the
use cases, and views import neither `org` nor `conversation`. From `kernel`
they hold only `kernel.ID`, which carries no behaviour a template could
misuse. Components do no I/O and
make no authorisation decisions; presentation logic (building URLs,
formatting, choosing an i18n message ID) belongs in them.

## htmx

htmx does only requests and swaps; the server and templ own the HTML.

- A normal request gets a full page. Selecting part of a server-rendered
  full page with `hx-select` is allowed; Load older does this today.
  Enhanced posts return only the `MessageComposer` fragment. On success its
  `data-posted-message` carries the posted message's DOM id; errors omit it.
  Latest composers carry a hidden `cursor`; plain forms use the page snapshot.
  Composers on `?before=` pages omit it, so posting reads only the author's own message.
  `message-composer-v4.js` copies `#organization-stream`'s applied-and-shown
  `data-event-cursor` on each submit only when the form has a cursor field,
  including after composer replacement; it never adds one.
  Fragment responses echo the submitted cursor, never the new post's sequence;
  only successful stream application advances the outer cursor. The action URL
  identifies the feed/topic scope. The stream script consumes that id to scroll once the item is present,
  regardless of response/delivery order or a reconnect.
- A unit that M3's SSE needs becomes an explicit templ fragment or
  component, rendered by the same code as the full page. `MessageItem`
  takes a `view.Message` and renders one `<li>` on its own or in the page.
- **DOM ids are a contract.** Templates define every id that `hx-target`,
  `hx-select`, `hx-select-oob` or a script refers to. Renaming one means
  updating every reference in the same pull request. Today's contract ids
  are `organization-stream`, `stream-sink`, `conversation`, `message-list`, `message-items`, `load-older`,
  `message-body`, `message-composer`, `message-help`, `message-status`,
  `read-form`, `members-panel`, `members-list`, `branch-form`, `branch-to`, `branch-name` and `branch-feedback`, plus
  `message-<hex>` from `MessageDOMID` and `select-message-<hex>` for its
  checkbox, `channel-<hex>` from `ChannelDOMID` and `topic-<hex>` from
  `TopicDOMID` for sidebar `<li>` entries, and `presence-<hex>` from `PresenceDOMID` for member indicators
  (32 lowercase hex digits). `#message-items` is always present,
  including when empty; Load older selects its direct `<li>` children.
- **The data attributes that scripts read are a contract too,** with the
  same rule for renaming (#351). A missing one fails silently: without
  `data-oldest-seq`, the move handler throws after cancelling the SSE event,
  so the move is neither applied nor swapped. Besides `data-event-cursor`,
  `data-posted-message` and `data-announcement` (described in this section):

  | Attribute | On | Present | Meaning |
  |---|---|---|---|
  | `data-applied-cursor` | `#organization-stream` | after durable application | newest sequence applied to the DOM, including while hidden; reconnects use it |
  | `data-oldest-seq` | `#load-older` | always | `event_seq` of the oldest loaded message, `0` once nothing older remains; a moved item is inserted only at or above it |
  | `data-topic` | `#message-items` | topic pages only | the topic's ID, compared with a move's topics |
  | `data-from-topic`, `data-to-topic` | a `messages-moved` payload's `<ul>` | always | the move's source and destination topic IDs |
  | `data-event-seq` | each message `<li>` | always | its `event_seq`, which orders an inserted item |
  | `data-source` | each message's checkbox | always | its topic's ID; a selection stays within one source topic |
- Every channel/topic or members page, including `?before=` pages, carries
  `data-event-cursor` on `#organization-stream`, the outer layout div outside
  every swap target. It wraps the sidebar and conversation or members panel, with `hx-ext="sse"`
  and `sse-connect="/organizations/<slug>/events?after=<cursor>&want=<interests>&channel=<id>"`
  (plus `topic=<id>` on latest topic views). Latest pages declare
  `sidebar,messages,typing`; older pages declare `sidebar`; members pages declare `sidebar,presence` with
  `presence-after` from the same snapshot as their indicators. Both omit `topic`.
  Members navigation and paging use ordinary links; `#members-list` is bounded
  to 100 names and has no live list swap or live region. Presence entries replace
  individual labeled indicators by stable DOM id.
  See [stream scope](streaming.md#stream-scope). `reset` handling attaches to
  this container. Its reconnect cursor and `data-applied-cursor` advance after an inserted message
  settles or a move applies successfully. `data-event-cursor` advances only while
  visible, or when a hidden application is subsequently shown.
  Its hidden, non-focusable `#stream-sink` receives `sidebar,presence` with
  `hx-swap="none"`: htmx applies payload entries out of band by DOM id, ignoring
  entries absent from the page. The sink is hidden from assistive technology.
  Only latest pages' `#message-items` receives `message,messages-moved`; moves
  replace feed IDs or remove/insert topic IDs within the loaded range. Replacements clear selection and refresh
  branch constraints. `LiveMessageItem` shares `MessageItem` markup, adding
  templ-rendered `data-announcement` text only to stream payloads; the
  render cache stores those live items, attribute included. History pages,
  including Load older, omit it.
  `#message-status` retains only the latest 10 announcements; history itself
  is never a live region.
- Latest feeds/topics render `#read-form` with the snapshot `cursor`, a plain
  *Mark as read* submit button, and `hx-post` to the conversation URL plus
  `/read`: `POST /organizations/{slug}/channels/{channelID}/read` or
  `/organizations/{slug}/channels/{channelID}/topics/{topicID}/read`. `hx-trigger="read-visible, submit"`, `hx-sync="this:drop"` and `hx-swap="none"`
  send automatic and manual POSTs without swapping history or overlapping reads.
  Members and `?before=` pages omit it. The endpoint resolves visibility,
  calls the transaction-owning unread use case and returns 204 to htmx or a 303
  for the plain form.
- The layout's `htmx-config` meta tag holds htmx's security settings and
  `responseHandling` ([`rendering.md`](rendering.md)). It keeps the three
  defaults, adding global 409/422 swaps (`error: false`) before `[45]..`.
  Composer errors replace `#message-composer`; branch errors replace only
  `#branch-feedback`, so selections and drafts survive.

## JavaScript

JavaScript never generates HTML and never owns application state. It is
limited to UX help that HTML and htmx handle poorly: focus, scroll,
keyboard, local time, single-source selection constraints, and glue for SSE.

- `message-stream-v8.js` emits `read-applied` only after successful DOM
  application is shown while visible, including on return from a hidden tab.
  `read-visibility-v3.js` initializes on scoped `htmx:load` and watches that
  event and visibility. It copies the applied cursor (initially the form's
  snapshot) into htmx's read request. While one request runs, triggers coalesce;
  after success it triggers only the newest shown cursor, only if visible.
  Only successful completion records a cursor as sent; failure flushes a strictly
  newer shown cursor, but resending the same cursor waits for the next visibility
  or application trigger.
  Composer responses and received-but-unapplied events cannot advance it.
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
