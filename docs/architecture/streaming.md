# Streaming

## Server-Sent Events

One SSE connection per latest channel page (M3) or topic page (#304),
carrying named events:
`message`, `messages-moved` and `reset` today; `presence`, `typing`
and `unread` are planned (M4). The browser sends everything else as ordinary POST requests. One
process serves every stream; several server processes are future work (the
hub, the per-account cap and the watermark are per process).

### Ordering and replay

```mermaid
sequenceDiagram
  participant B as Browser
  participant W as web
  participant DB as PostgreSQL
  participant C as connection goroutine
  B->>W: GET page
  W->>DB: REPEATABLE READ, READ ONLY: page data + event_seq of this organisation
  W-->>B: HTML with cursor = event_seq
  B->>W: GET /events?after=cursor
  W->>C: start
  loop
    C->>DB: one snapshot: replay boundary + event_seq + events after cursor
    C->>C: check interest, render, authorize, send; cursor = last seq read
    C->>C: wait until hub's latest sequence of org > cursor (no wait if already)
  end
```

- The initial HTML and the cursor are read in **one `REPEATABLE READ READ
  ONLY` transaction**. Under PostgreSQL's default `READ COMMITTED`, each
  statement sees a new snapshot, so a message committed between reading the
  messages and reading `event_seq` would be missing from the page *and*
  skipped by the stream.
- **No lost wakeups.** Waiting means "block until the hub's latest
  sequence for this organisation is greater than my cursor", and that
  condition is checked under the hub's lock *before* blocking. The hub's
  value is level-triggered: if event 101 is committed after the connection
  read `seq > 100` and found nothing, but before it starts waiting, the hub
  already holds 101, so the wait returns at once and the next read delivers
  it. Wakeups are broadcast (a condition variable, or a channel that is
  closed and replaced on every raise), never a buffered per-connection
  signal that could be dropped. The cursor advances to the last sequence
  *read*, including events this connection may not see, so a filtered event
  cannot keep the loop spinning.
- **The hub** (`realtime.Hub`, #157) implements this: `Raise(org, seq)`
  only raises the value, and `Wait(ctx, org, after)` returns the value
  once it is above `after`, or `context.Cause(ctx)`. It is also the connection
  registry: `Register(parent, connection, limit)` refuses atomically once
  the account holds `limit` connections. Otherwise it returns a context
  derived from the request's context, which `CancelAccount` or
  `CancelSession` can end, and an `unregister` the handler defers. Only `unregister` frees the slot;
  `context.Cause` tells why a connection ended.
- **The loop** is `realtime.Stream.Run` (#209). `Subscription` owns the
  interest checks: other channels, unsupported kinds and other posting-time
  topics are skipped before rendering or authorization (#261 B6). Explicit
  denies from `authz.MayReceive` are skipped after rendering. All skips advance
  the cursor. `messages.moved` reaches feeds and its source/destination topics
  as `messages-moved`, with one shared batch render and per-connection
  authorization. Other topics skip before either. Feeds replace loaded IDs;
  source topics remove them, destinations insert by `event_seq` within the
  loaded range. The server-rendered history control holds the oldest sequence
  (zero when no older history remains, including an empty page); live changes
  never move it or its `before=` link. Only
  Load older replaces it, so older moved items cannot duplicate or skip history.
  Moves are silent; the notice arrives separately as `message.posted`.
- **A topic page** subscribes to one topic of the channel
  (`…/topics/{topicID}/events`; an unknown topic, or another channel's, is
  404 before the stream opens). New posting events route by their persisted
  posting-time topic, even when the render reads a later topic. Legacy events
  without `topic_id` fall back to the topic from the shared render, before
  authorization, with no extra read per stream. Move routing uses the event's
  source and destination, independently of the rendered item's current topic.
  The cursor, replay
  and `reset` rules are the channel page's. An error from the reader, the
  authorization check itself, the renderer or the sender stops the loop
  with the cursor before that event, so a reconnect resumes there; a failed
  membership lookup is never a deny. A render error stops the loop even for
  an event the check would have denied, since the check comes after it.
  Cancellation is checked before every event, so an ended session sends
  nothing more. It drains every batch before waiting.
- **Missed raises** (#237). Posting raises the hub right after its commit,
  but a writer without a notifier (`cmd/seed`, sign-up's `member.joined`)
  or, later, another process commits without one, and a stream that has
  caught up would wait until the next post. `realtime.Watermark` reads, every
  `WatermarkInterval` (5 s) and in one query, the committed `event_seq` of
  the organisations with registered connections (`Hub.ActiveOrganizations`)
  and raises the hub for those still active (`RaiseIfActive`, under the
  hub's lock). It only wakes streams; they read from their cursor, so
  `event_log` stays the only truth, and such an event arrives after the next
  successful check. Each check's query has a 2 s timeout; a failed check is
  logged and retried on the next tick; without connections it reads nothing.
- Its cost per post grows with the number of open streams; measured in
  [`stream-cost.md`](stream-cost.md).
- Replay and live delivery go through the same per-connection loop, so they
  cannot interleave out of order. `Last-Event-ID` is preferred on reconnect;
  before htmx recreates the `EventSource`, the client puts its last cursor
  into `after`. DOM updates are idempotent (elements are replaced by id), so
  a duplicate delivery is harmless.
- A cursor below `organization.event_log_boundary_seq` or above the committed
  `organization.event_seq` gets one `reset` and the loop returns without
  advancing it or sending later events. The upper check covers a browser
  whose cursor is ahead of the log after a restore made with ribbitto stopped
  ([Restoring a backup](../../README.md#restoring-a-backup)); rolling the
  database back under a running process is not supported. Each batch,
  including on open streams and cache hits, checks both bounds; a sequence
  gap anywhere in a batch also resets, before any of it is delivered. Both
  bounds are inclusive, even with an empty log; at `event_seq` the stream waits normally.
  The versioned message-stream script listens for `reset` on htmx's source,
  closes it and reloads the page; this is SSE glue with no separate request.

### Retention

`realtime.Retention` expires replay history once at start and then hourly.
The worker starts asynchronously before serving, so its first run can overlap
requests. It takes the same organisation lock as posting; separate transactions
of at most 1,000 expired rows and a one-minute run timeout limit contention.
Failed runs retry on the next tick, keeping committed progress. Shutdown cancels
and waits for the worker, including a blocked first run, before closing the pool.
The retention period defaults to seven days; see [database configuration](../database.md).

### Authorization and revocation

- Before registration, the handler rejects a present `Sec-Fetch-Site` other
  than a single `same-origin` value with 403, including `same-site`,
  `cross-site` and `none`; it takes no stream slot. An absent header remains
  allowed for older clients and tools. This supplements
  `http.CrossOriginProtection`, which exempts GET requests.
- Every event is authorized for the connection's member **after it is
  rendered and immediately before sending**, including events already
  queued: access may have been lost in between, also while a render waits on
  the database (#262). The check stays one membership query per event; a
  render that is then denied is discarded, and renders are shared and cached
  anyway. Only the write itself remains between the check and the
  connection. The check itself is `app`'s authorization, reached through the
  `Authorizer` interface that `realtime` defines.
- A stream registers with the hub under its session (#207), then looks the
  session up again, so a sign-out in between still stops it. Deleting a
  session (sign-out, or a sign-in replacing it) cancels that session's
  streams at once; the stream's context ends when the session expires.
  Other sessions of the account stay connected. The handler's private
  `openStream` helper owns registration, the re-check and the expiry deadline.
  It frees failed registrations; on success, the handler defers one cleanup
  function that cancels the expiry context and unregisters the stream.
  Cancellation before the first write answers 404 for an ended session or
  503 for shutdown, without committing SSE headers. The status is
  committed by `WriteHeader` outside the sender, after one more look at the
  context: the sender expires the write deadline on cancellation, which
  over HTTP/2 resets the stream, so the answer would never arrive. A
  cancellation after `WriteHeader` (a window no lock can close) ends a
  stream that has started, without trying to change its HTTP status
  (maintainer's decision on #315).

### Resource limits

Cache-load limits, write deadlines, heartbeats, the per-account stream cap,
shutdown and HTTP/2 are in [`stream-limits.md`](stream-limits.md).
