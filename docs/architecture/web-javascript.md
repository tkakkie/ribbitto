# Web JavaScript

JavaScript responsibilities and constraints for browser enhancements.

## JavaScript

JavaScript generates no HTML and owns no application state. It supplies focus, scroll,
keyboard, local time, selection constraints, copying to the clipboard, and
htmx/SSE input glue. The bounded composer state of decision 37 is the
[composer](web-composer.md#composer) exception. A UI-library island is only a scoped
exception under
[decision 40](../decisions/40-plain-javascript-by-default-library-islands-by-measured-exception.md);
none exists today.

**Exception (#1039):**
[decision 34](../decisions/34-live-unread-counts.md) lets one script own
sidebar badge numbers and bounded recovery state: the snapshot sequence,
at most 1,000 retained `unread` events with eligibility and application flags,
a recovering flag, loss generations and the highest possibly lost sequence.
It changes only the text and visibility of server-rendered badges, never
creates elements or makes requests;
htmx sends resync requests. The markup contract is in
[web contracts](web-contracts.md).

- `message-stream-v13.js` observes `checkpoint` to update only `sse-connect`'s
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
- `member-mentions-v1.js` marks resolved references whose `data-member-id`
  matches `#organization-stream`'s existing `data-member`, on scoped
  `htmx:load` (including htmx's initial body event after all deferred scripts).
  Live posts, duplicate replay and moves use `htmx.swap`;
  Load older selects and prepends server-rendered items. Both run htmx's
  inserted-node load tasks, including for replacement and destination insertion.
  No DOM observer or polling is needed. Token utilities add a 2 px dashed `brand`
  outline with no offset, distinct from the solid, 2 px offset focus ring:
  a shape cue with at least 3:1 contrast against the message background
  and `selected` chip in both themes (see [colour contrast](../ui-colours.md#contrast-wcag-22-aa)).
  The shared server render stays identical for every reader; unresolved and
  other-organisation references carry no ID. Quote snapshots remain plain text.
- `message-link-v1.js` handles "Copy link" in a message's actions menu
  (#1159): it copies the link's browser-resolved absolute `href` with the
  Clipboard API, so shared live renders hold no origin; marks the link
  `data-copied` (which shows "Link copied") until its menu closes; and
  appends that label to the polite `#message-status`. With a modifier key,
  without the API, or when copying fails, the link opens, as without
  JavaScript.
- SSE `reset` closes htmx's event source. A DOM observer holds reload while
  the composer has `htmx-request` or #1179's failure alert, or a pending form
  is queued, sending or failed (including unconfirmed). A localised status
  offers a plain reload link. With nothing at stake, one uniform 0–5 s timer
  reloads for a fresh snapshot; new at-stake state cancels it, and firing
  rechecks. Confirmed items await reload delivery; drafts do not hold it.
- **No requests of its own:** no `fetch`, `XMLHttpRequest`,
  `navigator.sendBeacon`, `WebSocket`, new `EventSource` or `htmx.ajax`.
  Requests go through HTML forms, links and htmx. A script may set
  form-control values and call `requestSubmit()`, `htmx.trigger` and
  `htmx.process` on server-rendered or template-cloned elements; request
  URLs come from server-rendered attributes, never built by a script. The
  one kept rewrite is `message-stream-v13.js` updating only the `after`
  parameter of the server-rendered `sse-connect` URL (above).
- **Storage:** `sessionStorage` may hold only non-sensitive UX settings
  whose loss affects no function and no server state, such as whether a
  panel is open or a display preference; `localStorage` only the one
  [drafts](web-composer.md#drafts) allowance, in the drafts script alone
  ([decision 37][d37]). They never hold messages, credentials, session
  tokens or other server-derived application data, except that allowance's
  bounded draft text (which may quote messages), owner metadata and
  sign-out notice. IndexedDB is allowed only by
  [decision 41](../decisions/41-client-storage-indexeddb-by-data-class.md)'s
  data classes.
- **Files:** every script is external in `web/static` and loaded with the
  response nonce. Application scripts also define no globals and are
  versioned by file name (`-vN`), since static assets are immutable
  ([`rendering.md`](rendering.md)). Vendored libraries (htmx, idiomorph)
  keep their own globals and upstream file names ([a11y]).
- **htmx events:** an `htmx:load` handler works only on the inserted element
  and descendants, never the whole document (#168).

## Composer

The [composer queue](web-composer.md#composer) holds at most 10 pending items
and dispatches in FIFO order, pausing while an outcome is unknown. Retry of
a Not sent item goes to the end; an unknown outcome's Retry keeps its place.
Edit restores a failed item's text and key only into an empty composer outside
IME composition; it never overwrites a draft. It removes that item, frees its
slot and resumes a paused queue. Otherwise the visible, disabled button
describes why. The restored key survives other responses' state swaps;
sending transfers it to the pending form, and emptying the textarea renews it.
Unused keys post normally; equivalent validated bodies return the stored ID;
a different body under a used key gets 409 and a fresh key for Retry.
Topic pending items settle by confirmed ID, response topic/applied sequence, or a subsequent
move from the shown topic; the script retains no move history
([decision 37][d37]).

[a11y]: ../accessibility.md
[d37]: ../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md
