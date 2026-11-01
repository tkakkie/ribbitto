# Web JavaScript: unread

Unread-script contracts for [web JavaScript](web-javascript.md#javascript).

`message-stream-v14.js` parks only a non-CLOSED source reported by
`htmx:sseClose` during persisted `pagehide` cleanup of `#organization-stream`.
Persisted `pageshow` dispatches `error` on that closed source once, then
forgets it. The vendored SSE extension retries after 500 ms, reads the current
`sse-connect` (including `after`), and rebinds the children. This behaviour is
version-pinned: extension upgrades must rerun `TestStreamBFCacheBrowser`.
No internal source slot, reprocessing or independent connection is used.
The cleanup is a bubbling `htmx:beforeCleanupElement` on `#organization-stream`
that every listener sees, so such listeners (`sidebar-unread-v3.js`) must leave
state usable for the next persisted `pageshow`.
Recreated sources have no `Last-Event-ID`; replay, deduplication and reset
remain unchanged. Already-CLOSED sources are not parked; any pending
extension retry remains its only retry.

- `message-stream-v14.js`: `checkpoint` updates only `sse-connect`'s `after`, monotonically, independent of applied/shown cursors. `read-applied` emits only after successful DOM application is visibly shown, including hidden-tab return. `web/src/read-visibility.ts` (`web/src/read-cursor.ts`) initializes on scoped `htmx:load`; automatic triggers send snapshot then latest applied cursor. One request/timer per page coalesce
  cursors. A 429 retries until success/hide/removal/navigation: jittered exponential backoff, initial 8 s cap; `Retry-After` is a minimum even above it. Success resets backoff, flushes newer shown work; other failures flush strictly newer work only. Same cursor awaits another trigger. Cleanup clears timers. Composer responses/unapplied events cannot advance it.
- `sidebar-unread-v3.js` attaches to scoped `htmx:load` and SSE open events.
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
  Cleanup of its stream container, including the persisted `pagehide` one, clears and forgets its timer.

  | Attribute / target | Contract |
  |---|---|
  | `data-member` on `#organization-stream` | session-resolved member UUID for own-post exclusion |
  | `data-sidebar-topic` on that container | selected topic UUID, present only on topic pages; routes the unlisted heading badge |
  | `data-snapshot-seq` on `#sidebar-channels` | exact counts' organisation sequence; replaced by every snapshot |
  | `data-unread-count` on every badge | raw numeric count, including hidden zero; only its number, text and visibility change |
  | `#sidebar-resync` | hidden htmx trigger; custom `sidebar-resync` event sends the member-scoped POST, without a swap |
