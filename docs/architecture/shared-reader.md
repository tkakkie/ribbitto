# Shared reader

*Planned* (#232): one reader per organisation reads new events once, and
each connection pulls them from the reader's bounded window instead of
reading the log itself. Whether to build it is decided separately, on the
evidence in [Deciding](#deciding-whether-to-build-it). How it grows, and
its implementation issues, are in [growth](shared-reader-growth.md).
Today's loop is in [replay](replay.md).

## Today

Each connection runs `realtime.Stream.Run`: it reads through
`CachedEvents`, delivers, and waits on the hub. A post wakes every
connection of its organisation, and each one reads the hub's atomic
durable level/channel snapshot and looks up the event cache, joining a shared
read and, for the short batch that holds a new post, a second shared read
after it. The writer's combined wait also includes its declared ephemeral
generations, heartbeat and context (#735); production presence is current (#768); typing remains planned (#288).
Renders, authorization and sends are per connection.

## The reader

- **Lifecycle.** `realtime` keeps a registry of readers, one per
  organisation with streams. `Stream.Run` acquires its organisation's reader
  when it starts and releases it when it returns, so both fall between the
  handler's `openStream` (registration and the session re-check, #207) and
  its deferred cleanup: a refused or cancelled registration (429, 503, 404)
  never starts one. The first acquire starts the reader;
  it reads the committed `event_seq` through the watermark's
  `SequenceReader` and starts its window there (or at the floor below, if
  higher), so it never replays history. The last release stops it and drops the window; a later acquire
  starts a new one. Readers run under the process context: shutdown ends
  every stream (`Hub.CancelAll`), the releases stop the readers, and
  `cmd/ribbitto` waits for their goroutines before closing the pool.
- **Loop.** The reader waits on the hub as a stream does today (the level
  above both its head and the last value seen), reads `EventsAfter(head,
  batch)` from the store directly, not through `CachedEvents`, checks that
  the batch continues its head, appends it and publishes the new head. It
  drains full batches before waiting. The hub and the watermark (#237) now
  wake one goroutine per organisation: the reader.
- **Window.** A contiguous range `(lo, hi]` of event envelopes exactly as
  read, without renders, bounded by count and evicting the oldest on
  append, whoever has not read them. It holds 256 events per organisation:
  today's event cache holds 1,024 entries for the whole process, so the
  same number per organisation would mean far more memory. The measurement
  issue records misses and memory, and raises it to 512 or 1,024 if needed.
  No age bound: an idle organisation keeps its last events, which are
  immutable and stay valid through the floor below. `hi` is the reader's watermark; publishing closes and replaces a
  channel under the reader's lock, so a waiter cannot miss it. Today's hub
  publishes a level/channel snapshot atomically and closes the old channel.
- **Floor.** Retention is the only writer of the replay boundary, and runs
  in this process. The wiring wraps org's `RetentionBoundary` so that
  `RaiseBoundary`, which the cleaner calls inside its transaction before
  committing, first raises the organisation's floor in the registry, which
  keeps it while the organisation has no reader, too.
  Raising it evicts every entry at or below it, and appends never go below
  it, so `lo` is at least every boundary already committed. A rolled-back
  batch only leaves a floor too high, which sends reads to the database.
- **Failure.** An ordinary start or read error is retried up to three
  times within one deadline of about 2 s that covers the attempts as well
  as the backoff: each attempt's context is bounded by the time left, since
  the store and sequence readers set no deadline of their own. Meanwhile
  streams keep their cursors and send heartbeats. `ErrCursorExpired`, a gap, and
  cancellation or shutdown end the reader at once, without a retry. A
  reader that ends this way or runs out of retries fails: it records the
  error, wakes its waiters and leaves the registry. Every stream holding it
  checks that failure before every event, with its cancellation, so it
  stops before its next event with its cursor unchanged, even inside a
  copied batch, and acquires a fresh reader on reconnecting. The reader
  never skips or sends `reset`; each stream decides that from its cursor.

Decision 23 holds: the window is a cache of `event_log` rows read after
their commit, like `CachedEvents`, not a fan-out from memory. A missed
raise only delays the reader; a restart loses the window, and browsers
replay from the log.

## Hand-over from replay to live

`Stream.Run` keeps its loop. Only where it reads and what it waits on change:

- **Read at cursor `c`.** Under the reader's lock the stream takes `lo` and
  `hi`; if `lo ≤ c ≤ hi` it copies the events `(c, min(c + batch, hi)]` and
  releases the lock. Otherwise — `c < lo` (a replay, a connection that fell
  behind, or a cursor below the floor), `c > hi` (a reader not started yet
  or behind the log) — it reads the **database path**, today's
  `CachedEvents` read with its bounds checks, `reset` and gap detection.
- **Wait only on the reader:** until `hi > c` (a reader still starting
  counts as below every cursor), its failure, or the connection's context;
  heartbeats as today. A connection never waits on
  the hub itself. Mixing the two would stall: a stream on the database path
  that saw the hub at 105 and then read 104 from a window whose `hi` is 104
  would wait for the hub to pass 105; the reader appending 105 raises
  nothing, so 105 would wait for the next post.
- **No gap, no duplicate.** A connection has one cursor. Each read, from
  either source, returns exactly the contiguous run after it (the window is
  a contiguous copy of the log; the database path checks contiguity), and
  the cursor moves only by #209's rules. The source is chosen per read from
  one snapshot of `lo` and `hi`, and the batch is copied before the lock is
  released, so a later eviction cannot change it. If the reader advanced
  during a replay, the switch simply happens at a higher cursor; if
  eviction passed the cursor, the next read goes back to the database.
- **Cursor validity (decision 24), unchanged.** Today every batch is
  validated by a database snapshot taken after the stream asked for it.
  Each window read is validated when it is copied, without a query:
  `c ≥ lo`, which is at least every committed boundary, and `c ≤ hi`, which
  is at most the committed `event_seq`. A cursor whose events retention has expired is
  below the floor, so it reads the database and gets `reset`, short batch
  or full. Validity is decided at the copy, and delivery may take longer,
  each send up to its write deadline, as with today's batches after their
  read. A restore still requires ribbitto to be stopped.
- **#209's rules are unchanged:** skips advance; a reader (now also the
  shared reader's failure), authorization, render or send error stops the
  loop with the cursor before that event; cancellation is checked before
  every event; render, then authorize, then send.

## Render variants and authorization

The window holds envelopes, not output. Each connection renders through
web's shared render cache, keyed by organisation, channel, sequence and
**language** (#227): one render per event per language, joined by every
connection of that language, never one payload for everyone.
Authorization stays per connection, after the render and immediately
before the send: `MayReceive`, a cached allow checked against a fresh
shared epoch (#670). The subscription's filter stays a pre-filter.

## Slow connections

No per-connection send queue (decision 23):

- The reader appends and evicts without looking at connections; a
  connection holds only its cursor. It holds the lock only to copy a batch
  and writes outside it, so a slow connection never blocks the reader or
  another connection.
- A slow connection only falls behind its own cursor. Once `c < lo`, it
  reads the database like a replay, shared through `CachedEvents` with
  streams at the same cursor.
- A write past its deadline (10 s, #160) ends that stream; the browser
  reconnects and replays from `event_log` with its `Last-Event-ID`.
- #207's registration, session cancellation and expiry, and #160's caps,
  heartbeats, shutdown and cleanup work as today; acquiring and releasing
  the reader sits inside them.

## Deciding whether to build it

What #671 measured with cached allows
([results](stream-authorization-results.md)):

- **Benchmark:** with one member, 16 to 27 queries per post from 1,000 to
  30,000 streams; 40,000 failed with the pool never empty, and the benchmark
  cannot see CPU. With distinct members the limit was the authorization
  cache's capacity, which the reader does not touch.
- **End to end (#216's steps):** HTTP/1.1 passed 15,000 and failed 20,000.
  The server used about 4.1 CPUs at both and available memory fell to
  0.9 GiB; the pool was not the cause, and the run did not isolate whether
  CPU work, serialisation or memory came first.

**Expected change.** Per post, one event read per organisation instead of
about four through the shared cache (#227's count). Window reads run no
query, not even a bounds check. Per connection, a copy under the reader's
lock replaces the hub's atomic snapshot read, an event-cache lookup and a joined
second read. Unchanged per connection: a wake for every connection of the
organisation (until the interest index), the render-cache lookup,
`MayReceive`, the write and flush, the goroutine and its buffers. Queries
per post fall by a few at most, and the pool was not the limit; any gain is
CPU and lock contention, which no run has measured. On present evidence it
is not expected to move the measured limits by itself.

**Evidence that should decide:**

1. CPU and mutex profiles of `TestStreamCost` at the one-member 30,000 and
   40,000 steps (`_CACHE=1`, `go test … -cpuprofile cpu.out -mutexprofile
   mutex.out`, no code change): the share in the path the reader removes (`CachedEvents.EventsAfter`,
   the event cache, `Hub.Latest` and `Hub.Wait`) against renders,
   `MayReceive` and sends.
2. A CPU profile of the server at #216's 15,000 HTTP/1.1 step, separating
   SSE writes, garbage collection and memory from that path. It needs
   #695's profiling endpoint on the [development metrics](dev-metrics.md)
   listener (development only, never production).

**Criterion** (maintainer, 2026-10-09). Build it only if that path takes
at least 20% of the CPU profile or at least 25% of mutex blocking time,
and both profiles show it as the same hotspot. Under 10% of CPU and not a
mutex hotspot, do not build it. Between 10% and 20%, hold it and compare
it with #236's other options.
