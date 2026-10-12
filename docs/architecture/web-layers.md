# Web layers: handlers, templ, htmx and JavaScript

Browser-facing responsibilities, decided on 2026-09-30 ([decision 19](../decisions/19-web-layers-server-owned-html-htmx-swaps-javascript-as-enhancement.md)).

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

Components render view models defined in `internal/web/view`. Handlers convert module results; components never receive them. View
models own their `Organization` (`Slug`, `Name`), `Channel` (`ID`, `DefaultTopicID`, `Name`)
and `Topic` (`ID`, `Name`, `IsDefault`). Views import neither `org` nor
`conversation`. From `kernel`
they hold only `kernel.ID`, which carries no behaviour a template could
misuse. Components do no I/O and
make no authorisation decisions; display logic (building URLs,
formatting, choosing an i18n message ID) belongs in them.

## htmx

htmx does only requests and swaps; the server and templ own the HTML.

- A normal request gets a full page. `hx-select` can select part of it (Load older).
  Enhanced posts return `MessageComposer`: the message form plus a hidden
  `typing-form`, refreshed out of band with the same generation (only there: Load
  older would install an unclosed one). Signals use the
  actual posting topic's `/typing` POST, `typing-input throttle:2s queue:none`,
  independent `this:drop` synchronization and no swap. Input/IME glue only triggers
  htmx. On success the message form's
  `data-posted-message` carries the posted message's DOM id; errors omit it.
  Latest composers carry a hidden `cursor`; plain forms use the page snapshot.
  Composers on `?before=` pages omit it, so posting reads only the author's own message.
  `message-composer-v5.js` copies `#organization-stream`'s applied-and-shown
  `data-event-cursor` on each submit only when the form has a cursor field,
  including after composer replacement; it never adds one.
  Fragments echo the submitted cursor; only stream application advances it. The action URL identifies the feed/topic. The stream script consumes that id to scroll once the item is present,
  regardless of response/delivery order or a reconnect.
- SSE uses explicit fragments shared with full-page rendering. `MessageItem`
  takes a `view.Message` and renders one `<li>` on its own or in the page.
- **DOM ids are a contract.** Templates define every id that `hx-target`,
  `hx-select`, `hx-select-oob` or a script refers to. Renaming one means
  updating every reference in the same pull request. Today's contract ids
  are `organization-stream`, `stream-sink`, `conversation`, `message-list`, `message-items`, `load-older`,
  `typing-form`, `typing-status`, `unread-divider`, `message-body`, `message-composer`, `message-help`, `message-status`,
  `read-form`, `members-panel`, `members-list`, `branch-form`, `branch-to`, `branch-name` and `branch-feedback`, plus
  `message-<hex>` from `MessageDOMID` and `select-message-<hex>` for its
  checkbox, `channel-<hex>` from `ChannelDOMID` and `topic-<hex>` from
  `TopicDOMID` for sidebar `<li>` entries, `link-channel-<hex>` and
  `link-topic-<hex>` ids for focus restoration, `sidebar-channels`,
  `sidebar-topics` for whole lists and `topic-heading-count` for every topic heading, and `presence-<hex>` from `PresenceDOMID` for member indicators
  (32 lowercase hex digits). Load older selects `#message-items > li`.
- **Script-read data attributes are a contract too,** with the
  same renaming rule (#351). Besides `data-event-cursor`,
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
  (plus `topic=<id>` on all topic views). Latest pages declare
  `sidebar,messages,typing`; older pages declare `sidebar`; members pages declare `sidebar,presence` with
  `presence-after` from the same snapshot as their indicators and `panel=members`
  to suppress feed selection. Older topic pages retain `topic`.
  Members navigation and paging use ordinary links; `#members-list` is bounded
  to 100 names and has no live list swap or live region. Presence entries replace
  individual labeled indicators by stable DOM id.
  See [stream scope](streaming.md#stream-scope). `reset` handling attaches to
  this container. Its reconnect cursor and `data-applied-cursor` advance after an inserted message
  settles or a move applies successfully. `data-event-cursor` advances only while
  visible, or when a hidden application is shown again.
  Its hidden, non-focusable `#stream-sink` receives `sidebar,presence` with
  `hx-swap="none"`: htmx applies payload entries out of band by DOM id, ignoring
  entries absent from the page. Sidebar recounts use the page's list components,
  replacing the whole channel list, whose current channel nests its topics; an
  unlisted selection stays at its heading, outside the 50 topic links. Counts bypass the message cache.
  Only latest pages' `#message-items` receives `message,messages-moved`; moves
  replace feed IDs or remove/insert topic IDs within the loaded range. Replacements clear selection and refresh
  branch constraints. `LiveMessageItem` shares `MessageItem` markup, adding
  templ-rendered `data-announcement` text only to stream payloads; the
  render cache stores those live items, attribute included. History pages,
  including Load older, omit it.
  `#message-status` retains only the latest 10 announcements; history itself
  is never a live region. Latest feeds/topics receive `typing` on the stable
  `#typing-status` outside the composer, replacing only localized polite status
  content (`hx-swap="innerHTML"`). Empty reconnect frames clear stale content;
  typing changes neither focus nor durable cursors.
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

JavaScript generates no HTML and owns no application state. It supplies focus, scroll,
keyboard, local time, selection constraints, and htmx/SSE input glue.

- `message-stream-v9.js` emits `read-applied` only after successful DOM
  application is shown while visible, including on return from a hidden tab.
  `read-visibility-v4.js` initializes on scoped `htmx:load`; triggers send
  the snapshot, then the latest applied cursor.
  One request and one timer per page coalesce cursors. A 429 retries
  until success or hide/removal/navigation, with jittered exponential backoff
  capped initially at 8 s; `Retry-After` is a minimum, even above that cap.
  Success resets backoff and flushes newer shown work. Other failures flush only
  strictly newer work; the same cursor waits for another trigger. Cleanup clears
  timers. Composer responses and unapplied events cannot advance it.
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
  and descendants, never the whole document (#168).

Move/history regression tests use an installed headless browser through
go-rod (#349); see the [checks](web-layers-checks.md).

## How the rules are checked

The checks, the JavaScript pattern check and the M3 audit are in
[`web-layers-checks.md`](web-layers-checks.md).

[a11y]: ../accessibility.md
