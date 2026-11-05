# Web JavaScript: unread

Unread-script contracts for [web JavaScript](web-javascript.md#javascript).

During persisted `pagehide` cleanup of `#organization-stream`, park only a
non-CLOSED source reported by `htmx:sseClose`. Persisted `pageshow` dispatches
`error` on that closed source once, then forgets it. The vendored SSE extension
retries after 500 ms, reads the current `sse-connect` (including `after`), and
rebinds children. This behaviour is version-pinned: extension upgrades must
rerun `TestStreamBFCacheBrowser`. No internal source slot, reprocessing or
independent connection is used. Already-CLOSED sources are not parked; any
pending extension retry remains its only retry. Recreated sources have no
`Last-Event-ID`; replay, deduplication and reset remain unchanged.
The bubbling `htmx:beforeCleanupElement` on `#organization-stream` reaches every
listener; each must leave state usable for the next persisted `pageshow`.

- **Transport and visible reads:** `checkpoint` updates only `sse-connect`'s
  `after`, monotonically, independent of applied/shown cursors. `read-applied`
  emits only after successful DOM application is visibly shown, including
  hidden-tab return. Automatic reads send snapshot then latest applied cursor;
  composer responses and unapplied events cannot advance it. One request/timer
  per page coalesces cursors. A 429 retries until success/hide/removal/navigation
  with jittered exponential backoff; `Retry-After` is a minimum even above the
  backoff cap. Success resets backoff and flushes newer shown work; other
  failures flush strictly newer work only. Same cursor awaits another trigger.
  Cleanup clears timers. Backoff cases, including the 8 s cap:
  `web/test/read-cursor.test.ts`, `retryDelay: bounds jittered backoff and honours Retry-After`.
- **Sidebar counts:** `message` frames never increment. Missing rows wait for
  the next resync; malformed badges enter recovery. Sidebar snapshots must
  validate applied out-of-band badges before acknowledging application.
  A 429 retries with recovery open; losses during recovery reserve another
  request. Retention alone is not loss (`web/test/unread-counts.test.ts`).
  Badges and tab title/favicon update; htmx owns resync requests. Duplicate
  triggers merge into one pending request and one timer. Visibility return and
  persisted `pageshow` request resync after uniform 0–2 s jitter. Stream-container
  cleanup, including persisted `pagehide`, clears and forgets the timer,
  leaving state usable on return.

  | Attribute / target | Contract |
  |---|---|
  | `data-member` on `#organization-stream` | session-resolved member UUID for own-post exclusion |
  | `data-sidebar-topic` on that container | selected topic UUID, present only on topic pages; routes the unlisted heading badge |
  | `data-snapshot-seq` on `#sidebar-channels` | exact counts' organisation sequence; replaced by every snapshot |
  | `data-unread-count` on every badge | raw numeric count, including hidden zero; only its number, text and visibility change |
  | `#link-channel-*`, `#link-topic-*`, `#topic-heading-count` | server-rendered badge targets, resolved through the generated DOM contract |
  | `#sidebar-resync` | hidden htmx trigger; custom `sidebar-resync` event sends the member-scoped POST, without a swap |
