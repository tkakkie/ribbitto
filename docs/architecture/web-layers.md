# Web layers: handlers, templ, htmx and JavaScript

Browser-facing responsibilities, decided on 2026-09-30 ([decision 19](../decisions/19-web-layers-server-owned-html-htmx-swaps-javascript-as-enhancement.md)).

## Progressive enhancement

Core functionality works without JavaScript. JavaScript is progressive
enhancement, not full parity.

- **Core, today:** first-run setup, sign-up, sign-in and sign-out, opening
  a channel or topic with its history (including older pages) and its members or overview panel, posting a message,
  branching, pinning and unpinning a message, marking a feed or topic read, and creating channels or changing their images. Plain forms and links work alone;
  htmx and scripts improve them.
- **Enhancement only:** real-time updates, focus and scroll handling,
  keyboard shortcuts (Enter to send), local-time display, pending own posts on the feed and planned drafts on the device ([decision 37](../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md)). Without
  JavaScript a page may lack these, but nothing core breaks.

## Go handlers

Handlers in `internal/web` validate requests, call use cases, convert results
to view models, and choose the status, component and full page or fragment. They never build HTML strings
(`AGENTS.md`); plain-text responses such as `/healthz` are not HTML.

## templ

Components render view models in `internal/web/view`. Handlers convert
module results; components never receive them. View models own their
`Organization` (`Slug`, `Name`), `Channel` (`ID`, `DefaultTopicID`, `Name`,
`ImageURL`, `Image`, `Created`) and `Topic` (`ID`, `Name`, `IsDefault`).
Views import neither `org` nor `conversation`. From `kernel` they hold only
`kernel.ID`, which carries no behaviour a template could misuse. Components do no I/O and
make no authorisation decisions; display logic (building URLs,
formatting, choosing an i18n message ID) belongs in them.

## htmx

htmx does only requests and swaps; the server and templ own the HTML.

- A normal request gets a full page. `hx-select` can select part of it (Load older).
  Enhanced posts return `MessageComposer`: `#message-composer-state` (hidden
  controls and `#message-help`; never the textarea, #1173) plus a hidden
  `typing-form`, refreshed out of band with the same generation (only there: Load
  older would install an unclosed one). Signals use the
  actual posting topic's `/typing` POST, `typing-input throttle:2s queue:none`,
  independent `this:drop` synchronization and no swap. Input/IME glue only triggers
  htmx. On success the state's
  `data-posted-message` carries the posted message's DOM id; errors omit it,
  except a 409, which names the stored message and, when the text starts
  with its body, carries it in `data-sent-body` instead of the error. Sends time out after 30 s (`hx-request`). A field error sets its
  `data-invalid`, copied to the textarea's `aria-invalid`. Every composer has a hidden
  server-rendered `idempotency_key`: fresh on each render, kept on a 422.
  Latest composers carry a hidden `cursor`; plain forms use the page snapshot.
  Composers on `?before=` pages omit it, so posting reads only the author's own message.
  Feed pending items use the [composer contract](web-composer.md#composer).
  `message-composer-v10.js` copies `#organization-stream`'s applied-and-shown
  `data-event-cursor` on each submit only when the form has a cursor field,
  including after composer replacement; it never adds one.
  Fragments echo the submitted cursor; only stream application advances it. The action URL identifies the feed/topic. The stream script consumes that id to scroll once the item is present,
  regardless of response/delivery order or a reconnect.
- SSE uses explicit fragments shared with full-page rendering. `MessageItem`
  takes a `view.Message` and renders one `<li>` on its own or in the page.
- DOM ids and script-read data attributes follow the [htmx contract](web-contracts.md).
- Every channel/topic page (`?before=` and panel pages included) and the
  channel list page carry `data-event-cursor` on `#organization-stream`, the outer layout div outside
  every swap target. It wraps the sidebar, conversation and any panel (the list page: the list), with `hx-ext="sse"`
  and `sse-connect="/organizations/<slug>/events?after=<cursor>&want=<interests>&channel=<id>"`
  (plus `topic=<id>` on all topic views). Latest pages declare
  `sidebar,messages,typing`; older pages and the channel list page declare `sidebar`; the members panel adds `presence` with
  `presence-after` from the same snapshot as its indicators. Older topic pages retain `topic`.
  Panels open, switch, page and close with ordinary links (#874); `#members-list` is bounded
  to 100 names and has no live list swap or live region. Presence entries replace
  individual labeled indicators by stable DOM id.
  See [stream scope](stream-scope.md#stream-scope). `reset` handling attaches to
  this container. Its reconnect cursor also advances on transport checkpoints.
  `data-applied-cursor` advances after an inserted message
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
  *Mark as read* submit button only if something was unread (else the form is
  `hidden`), and `hx-post` to the conversation URL plus `/read` (`POST /organizations/{slug}/channels/{channelID}[/topics/{topicID}]/read`). `hx-trigger="read-visible, submit"`, `hx-sync="this:drop"` and `hx-swap="none"`
  send automatic and manual POSTs without swapping history or overlapping reads;
  automatic ones pause while a panel is modal ([panels](../ui-components.md#side-panels)).
  `?before=` pages omit it. The endpoint resolves visibility,
  calls the transaction-owning unread use case and returns 204 to htmx or a 303
  for the plain form.
- The layout's `htmx-config` meta tag holds htmx's security settings and
  `responseHandling` ([`rendering.md`](rendering.md)). It keeps the three
  defaults, adding global 409/422 swaps (`error: false`) before `[45]..`.
  Composer answers replace only `#message-composer-state`, branch errors only
  `#branch-feedback`, so selections and drafts survive.

## JavaScript

See [JavaScript responsibilities and constraints](web-javascript.md).

**Current exceptions** to the no-application-state rule:
[decision 34](../decisions/34-live-unread-counts.md) lets one script own sidebar
badge numbers and bounded recovery state, never creating elements or making
requests; the server still renders the badges and htmx sends resync requests.
[Decision 37](../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md)
allows bounded composer state (pending posts, idempotency keys, session-bound
drafts); implemented uses follow the [composer contract](web-composer.md).
Requests stay with htmx.

## How the rules are checked

[`web-layers-checks.md`](web-layers-checks.md) has the go-rod browser
tests (#349), the JavaScript pattern check and the M3 audit.
