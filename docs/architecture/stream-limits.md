# Stream resource limits

What bounds the cost of open Server-Sent Event streams. Ordering and replay
are in [`replay.md`](replay.md); authorization is in [`streaming.md`](streaming.md).

- Each cache (events, renders) runs at most 16 loaders at once, second
  loads included; a slot is held until its loader returns, even after a
  timeout. Waiting for a slot shares the 10 s load timeout, and a caller
  can leave sooner through its own context. The last waiter to leave cancels a load, the last joiner its
  second load, and shutdown every load (`cmd/ribbitto` passes the process
  context, through `Streaming` for renders).
  Results accepted after parent cancellation are not returned or stored,
  even before cancellation reaches the detached load context. Joiners of
  unkept results get the parent's cancellation cause if shutdown prevents
  their private second load from finishing.
  Joiners wait directly for their result, with one wake: a kept value or
  error from the first load, otherwise the private second load's result.
  Event-cache callers skip locked leave accounting once both results are
  published: the load has left the lookup map and its deferred cancellation
  ends the context. Until then, leaving still updates counts under the lock
  so the last waiter or last second-read joiner cancels pending work.
  Lookup, LRU, TTL and loading are unchanged; render caches keep their
  existing cleanup. Full batches alone are retained, short-batch joiners
  still get their private fresh second read, keys include the hub level,
  and every full batch still gets a zero-limit bounds read.

- Hub waiters read the organisation's level and broadcast channel in one
  immutable atomic snapshot, without the connection registry mutex.
  Organisation entries are never removed. Concurrent raises use compare
  and swap to publish only a higher level; the successful publisher alone
  closes the old channel. A raise between a waiter's snapshot read and its
  select therefore wakes it, and Latest never regresses. Register and
  RaiseIfActive still use the registry mutex for caps and active checks.

- Stream authorization caches allows per distinct (account ID, organisation
  slug), not per stream: streams with the same key share an entry. Entries
  stay after their streams close, until eviction; eviction is least recently
  used. A hit refreshes LRU at check completion if its captured entry remains
  resident; an eviction during the fresh epoch read does not resurrect it.
  When `RIBBITTO_AUTHORIZATION_CACHE_CAPACITY` is unset, `cmd/ribbitto`
  derives the capacity as `max(10,000, effective stream limit)`. The effective
  limit is `RIBBITTO_MAX_STREAMS`, or `realtime.DefaultMaxStreams` (5,000)
  when unset. With both unset, capacity stays 10,000. An explicit capacity
  must be a positive integer; empty or invalid values fail at start. A capacity
  below the effective stream limit is kept, with one startup warning naming
  both values: sharing a key makes a smaller capacity a valid choice (#692).
  Live keys fit in the derived capacity, but connecting and disconnecting
  members can still cause eviction and misses.
  [#671 measured](stream-authorization-results.md#benchmark-2026-10-09)
  churn past 10,000 distinct entries: hit rate fell to 0.905 at 11,000 and
  0.647 at 15,000. Each miss loaded a membership, and the pool limited delivery
  again (15,000 failed with p95 13.9 s).
  The epoch reader uses its own 16-loader
  limit, with a 5 s timeout and no retained values. `org.Authorizer.Stats`
  reports checks, membership-cache hits and actual epoch reads (including
  failures), so hit rate and sharing can be measured without changing the
  authorization rule.
  Token registration captures an immutable cached allow; the subsequent
  fresh epoch proves that snapshot even if it was evicted. Hits take two
  authorizer lock acquisitions, refreshing LRU in token cleanup only for
  the same resident entry. Misses can use a newer resident allow or query
  independently. Latest tokens still prevent older loads replacing newer
  results, including a missing-organisation deny. Deterministic #703 tests
  gate eviction to prove no reload or resurrection, check LRU at capacity,
  and count context Done evaluations to prove joiners wait once while
  still getting the fresh second read.

- Each connection reads the log itself (#209), so nothing queues for a slow
  client: its cursor just lags. A client that stops reading is
  disconnected when a write, event or heartbeat, misses its deadline
  (`DefaultStreamWriteTimeout`, 10 s; #160). The server's `WriteTimeout` bounds ordinary responses and
  would cut a long-lived stream, so the SSE handler does not inherit it: it
  sets a finite deadline before every write with
  `http.ResponseController.SetWriteDeadline`, flushes, and clears the
  deadline before waiting (#158) — under HTTP/2 an expired deadline resets
  the stream even while idle. If it cannot flush, the stream ends. The
  server's read timeout does not cut it: net/http clears the read deadline
  once the request is read. `TestEventStream` idles past all of them.
- Middleware that wraps `http.ResponseWriter` implements `Unwrap` so
  flushing works; compression is not applied to the SSE endpoint.
- An idle stream writes an SSE comment (`: heartbeat`) once
  `DefaultStreamHeartbeat` (20 s; #160) has passed since its last write,
  however often another channel's events wake it and while it drains a
  backlog of them: proxies keep it open, and a client
  that stopped reading is found out at that write's deadline. The loop
  sends it while waiting on the hub (`Stream.Heartbeat`); a failed one ends
  the stream without moving the cursor. Presence (M4) will wait about 30 s
  after a disconnect before showing a member as offline, so a reload does
  not flicker.
- The hub caps streams across all accounts and organisations in the process
  at `DefaultMaxStreams` (5,000; #623). `cmd/ribbitto` reads
  `RIBBITTO_MAX_STREAMS`: unset keeps that default; an explicit value must
  be a positive integer. Empty, zero, negative, unparsable and overflowing
  values fail at start. The default leaves headroom below #216's active
  ceiling: 7,000 passed and 10,000 failed at 10 posts/s (maintainer,
  2026-10-07). Smaller machines may set it lower; it can be re-evaluated
  if the measured ceiling rises. The hub checks both caps under its
  registration lock, refusing an excess stream with 429 before registration
  or any SSE header, without taking an account slot. Only unregistering
  frees a slot; cancellation alone still counts.
- An account holds at most `DefaultMaxStreamsPerAccount` (16) streams,
  counted atomically by the hub's registration (#157, #207). One more is
  refused with 429 before it starts, rather than closing the oldest, which
  would make that tab reconnect and close the next, forever. The htmx SSE
  extension retries a refused stream with back-off, 0.5 s doubling to 64 s,
  and connects once a slot frees. The count is per process; there are no
  limits per address or installation-wide.
- On shutdown, the server ends every stream first (`RegisterOnShutdown`
  with `Hub.CancelAll`, cause `ErrShutdown`): an open stream never goes
  idle, so `Shutdown` would otherwise wait for it until its deadline. The
  hub then refuses every registration, so a request accepted just before
  gets 503 instead of a stream.
  Browsers reconnect to the next process and replay from their cursor.
  Production serves HTTP/2 through Caddy, because browsers allow only six
  HTTP/1.1 connections per origin.
