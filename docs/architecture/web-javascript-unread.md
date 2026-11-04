# Web JavaScript: unread

Unread-script contracts for [web JavaScript](web-javascript.md#javascript).

The registered `message-stream` bundle (`web/src/message-stream.ts`) parks only a non-CLOSED source reported by
`htmx:sseClose` during persisted `pagehide` cleanup of `#organization-stream`.
Persisted `pageshow` dispatches `error` on that closed source once, then
forgets it. The vendored SSE extension retries after 500 ms, reads the current
`sse-connect` (including `after`), and rebinds the children. This behaviour is
version-pinned: extension upgrades must rerun `TestStreamBFCacheBrowser`.
No internal source slot, reprocessing or independent connection is used.
The cleanup is a bubbling `htmx:beforeCleanupElement` on `#organization-stream`
that every listener sees, so such listeners (`web/src/sidebar-unread.ts`) must leave
state usable for the next persisted `pageshow`.
Recreated sources have no `Last-Event-ID`; replay, deduplication and reset
remain unchanged. Already-CLOSED sources are not parked; any pending
extension retry remains its only retry.

- `web/src/message-stream.ts` uses DOM-free `web/src/stream-cursor.ts` decisions with case tables in `web/test/stream-cursor.test.ts`: `checkpoint` updates only `sse-connect`'s `after`, monotonically, independent of applied/shown cursors. `read-applied` emits only after successful DOM application is visibly shown, including hidden-tab return. `web/src/read-visibility.ts` (`web/src/read-cursor.ts`) initializes on scoped `htmx:load`; automatic triggers send snapshot then latest applied cursor. One request/timer per page coalesce
  cursors. A 429 retries until success/hide/removal/navigation: jittered exponential backoff, initial 8 s cap; `Retry-After` is a minimum even above it. Success resets backoff, flushes newer shown work; other failures flush strictly newer work only. Same cursor awaits another trigger. Cleanup clears timers. Composer responses/unapplied events cannot advance it.
- `web/src/sidebar-unread.ts` is the registered `sidebar-unread` bundle,
  before the stream script on channel, topic and channel-list pages. It attaches
  on scoped `htmx:load` and SSE open; `sidebar` frames swap through htmx and
  validate out-of-band badges before acknowledging application.
  DOM-free pure transitions in `web/src/unread-counts.ts` specify count updates,
  frame validation, eligibility, bounded retention, stream-order snapshots and
  loss-aware recovery with jittered backoff.
  A 429 retries with recovery open: [`429 request failure retries at backoff without completing recovery`](../../web/test/unread-counts.test.ts).
  Losses reserve another request: rows `new loss` / `repeated losses` of [`recovery needs both response and covering snapshot with no new losses`](../../web/test/unread-counts.test.ts).
  Retention is not loss: [`retention preserves eligibility without loss and ignores covered or duplicate frames`](../../web/test/unread-counts.test.ts).
  `message` frames never increment. Missing rows wait for the next resync;
  malformed badges enter recovery. The adapter owns badge text/visibility and
  `web/src/tab-unread.ts` projects channel totals into the title/favicon; htmx owns resync requests. Duplicate triggers merge
  into one pending request and one timer. Visibility return and persisted
  `pageshow` request resync after uniform 0–2 s jitter. Cleanup of the stream
  container, including persisted `pagehide`, clears and forgets the timer,
  leaving state usable on return. `TestSidebarUnreadBrowser` covers these DOM
  and lifecycle effects.

  | Attribute / target | Contract |
  |---|---|
  | `data-member` on `#organization-stream` | session-resolved member UUID for own-post exclusion |
  | `data-sidebar-topic` on that container | selected topic UUID, present only on topic pages; routes the unlisted heading badge |
  | `data-snapshot-seq` on `#sidebar-channels` | exact counts' organisation sequence; replaced by every snapshot |
  | `data-unread-count` on every badge | raw numeric count, including hidden zero; only its number, text and visibility change |
  | `#link-channel-*`, `#link-topic-*`, `#topic-heading-count` | server-rendered badge targets, resolved through the generated DOM contract |
  | `#sidebar-resync` | hidden htmx trigger; custom `sidebar-resync` event sends the member-scoped POST, without a swap |
