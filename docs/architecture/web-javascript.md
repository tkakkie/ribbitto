# Web JavaScript

JavaScript responsibilities and constraints for browser enhancements.

## JavaScript

JavaScript generates no HTML and owns no application state. It supplies focus, scroll,
keyboard, local time, selection constraints, copying to the clipboard, and
htmx/SSE input glue. The bounded composer state of decision 37 is the
[composer](#composer) exception.

**Exception ([#1039](../decisions/34-live-unread-counts.md)):**
[decision 34](../decisions/34-live-unread-counts.md) lets one script own
sidebar badge numbers and bounded recovery state: the snapshot sequence,
at most 1,000 retained `unread` events with eligibility and application flags,
a recovering flag, loss generations and the highest possibly lost sequence.
It changes only the text and visibility of server-rendered badges, never creates elements and never makes requests;
htmx sends resync requests. The markup contract is in
[web layers](web-layers.md#htmx).

- `message-stream-v11.js` observes `checkpoint` to update only `sse-connect`'s
  `after`, monotonically and independently of applied/shown cursors. It emits `read-applied` only after successful DOM
  application is shown while visible, including on return from a hidden tab.
  `read-visibility-v6.js` initializes on scoped `htmx:load`; automatic
  triggers send the snapshot, then the latest applied cursor.
  One request and one timer per page coalesce cursors. A 429 retries
  until success or hide/removal/navigation, with jittered exponential backoff
  capped initially at 8 s; `Retry-After` is a minimum, even above that cap.
  Success resets backoff and flushes newer shown work. Other failures flush only
  strictly newer work; the same cursor waits for another trigger. Cleanup clears
  timers. Composer responses and unapplied events cannot advance it.
- `sidebar-unread-v2.js` attaches to scoped `htmx:load` and SSE open events.
  It increments server-rendered badges only for newer, unseen `unread` frames
  from other members; `message` frames never increment. Snapshots apply in
  stream order, dropping retained events at or below their sequence. Eligible
  retained events above it reapply while not recovering, or when an answered
  resync has no loss since dispatch and its sequence covers every possibly lost
  event. Overflow records the highest dropped sequence; malformed or unapplied
  frames record the highest sequence seen, including a parsable failed frame.
  Retention itself is not loss. Overflow, malformed frames and DOM/request
  failures stop increments; losses during a
  request reserve another, including repeated losses during retries. One pending
  htmx request and one timer merge duplicate triggers, with exponential backoff
  starting at 250 ms and capped at 8 s; each retry chooses uniformly between
  half and all of that backoff. Visibility return and persisted `pageshow`
  request resync after uniform 0–2 s jitter. Recovery clearing is unchanged.
  A 429 counts as a request failure, keeps recovery open and retries
  through that backoff.
  Cleanup clears the timer when its stream container is removed.

  | Attribute / target | Contract |
  |---|---|
  | `data-member` on `#organization-stream` | session-resolved member UUID for own-post exclusion |
  | `data-sidebar-topic` on that container | selected topic UUID, present only on topic pages; routes the unlisted heading badge |
  | `data-snapshot-seq` on `#sidebar-channels` | exact counts' organisation sequence; replaced by every snapshot |
  | `data-unread-count` on every badge | raw numeric count, including hidden zero; only its number, text and visibility change |
  | `#sidebar-resync` | hidden htmx trigger; custom `sidebar-resync` event sends the member-scoped POST, without a swap |

- `pins-sync-v1.js` compares server snapshot sequences before pins/response
  swaps and selects loaded controls' server-rendered actions from the section's
  pin IDs. It restores button focus without touching history, draft or cursors.
- `message-link-v1.js` handles "Copy link" in a message's actions menu
  (#1159): it copies the link's browser-resolved absolute `href` with the
  Clipboard API, so shared live renders hold no origin; marks the link
  `data-copied` (which shows "Link copied") until its menu closes; and
  appends that label to the polite `#message-status`. With a modifier key,
  without the API, or when copying fails, the link opens, as without
  JavaScript.
- SSE `reset` closes htmx's event source and reloads the page after uniform 0–5 s jitter to obtain a fresh snapshot.
- **No requests of its own:** no `fetch`, `XMLHttpRequest`,
  `navigator.sendBeacon`, `WebSocket`, new `EventSource` or `htmx.ajax`.
  Requests go through HTML forms, links and htmx. A script may set
  form-control values and call `requestSubmit()`, `htmx.trigger` and
  `htmx.process` on server-rendered or template-cloned elements; request
  URLs come from server-rendered attributes, never built by a script. The
  one kept rewrite is `message-stream-v11.js` updating only the `after`
  parameter of the server-rendered `sse-connect` URL (above).
- **Storage:** `localStorage` and `sessionStorage` may hold only
  non-sensitive UX settings whose loss affects no function and no server
  state, such as whether a panel is open or a display preference, plus the
  one drafts allowance below
  ([decision 37](../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md)).
  They never hold messages, credentials, session tokens or other
  server-derived application data, except that allowance's bounded draft
  text (which may quote messages), owner metadata and sign-out notice. IndexedDB is not used; a clear need, such as offline
  support, gets its own issue.
- **Files:** every script is external in `web/static` and loaded with the
  response nonce. Application scripts also define no globals and are
  versioned by file name (`-vN`), since static assets are immutable
  ([`rendering.md`](rendering.md)). Vendored libraries (htmx, idiomorph)
  keep their own globals and upstream file names ([a11y]).
- **htmx events:** an `htmx:load` handler works only on the inserted element
  and descendants, never the whole document (#168).

## Composer

[Decision 37](../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md)
allows bounded composer state. Each part but the server-rendered key is
*planned*, awaiting its issue; today the composer script keeps none of it.

- **Pending own post** (planned, #1156; queue #1174): on enhanced latest
  composers only, a pending item cloned from a server-rendered `<template>`,
  body through `textContent`, in its own list beside `#message-items`. At
  most 10 per tab, DOM only; matched to its message through the response's
  `data-posted-message`. It never moves `data-event-cursor` or
  `data-applied-cursor`, reads, counts or announces.
- **Idempotency key** (#1155): the hidden `idempotency_key`, rendered by
  the server fresh on the initial render, after success and with 409, kept
  on 422. Scoped by the server to the authorised membership; never in a
  stream payload. Planned (#1156): the script may replace it with
  `crypto.randomUUID()`.
- **Drafts** (planned: #1178 sign-out and sign-in cleanup, #1181
  organisation-page cleanup, #1162 save and restore, #1177 sidebar marker):
  one drafts script keeps unsent text in `localStorage` under
  `ribbitto.draft.v1:<member>:<session>:<channel>:<place>` (the place is
  the literal `feed` or the topic's canonical UUID), at most 50 entries per
  member and session (best effort across tabs, #1186), 7 days after the
  last edit. It
  restores only its own member and session's unexpired entries, writes only
  before its session's deadline, and cleans up only entries strictly older,
  to the millisecond, than a time-ordered horizon (the page's session
  marker, or the sign-in page's PostgreSQL `uuidv7()`). In the sidebar it
  only shows or hides a server-rendered draft marker.
- **Send key** (planned, #1158 and #1175): a server-stored preference read
  from a rendered attribute, never browser storage.

[a11y]: ../accessibility.md
