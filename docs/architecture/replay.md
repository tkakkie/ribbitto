# Replay

How an event stream orders, replays and resumes events, and what it does when a message it names has vanished. The connection, retention and authorization are in [`streaming.md`](streaming.md); resource limits are in [`stream-limits.md`](stream-limits.md).

## Ordering and replay

```mermaid
sequenceDiagram
  participant B as Browser
  participant W as web
  participant DB as PostgreSQL
  participant C as connection goroutine
  B->>W: GET page
  W->>DB: REPEATABLE READ, READ ONLY: page data + event_seq of this organisation
  W-->>B: HTML with cursor = event_seq
  B->>W: GET /organizations/{slug}/events?after=cursor&want=interests
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
  condition and wake channel are read from one immutable atomic snapshot
  *before* blocking. A raise publishes a higher snapshot and closes the
  old snapshot's channel; a waiter holding that channel wakes even if the
  raise lands before its select. The hub's
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
  the process reaches its configured cap or the account holds `limit`
  connections. Otherwise it returns a context
  derived from the request's context, which `CancelAccount` or
  `CancelSession` can end, and an `unregister` the handler defers. Only `unregister` frees the slot;
  `context.Cause` tells why a connection ended.
- **The loop** is `realtime.Stream.Run` (#209). `Subscription` owns the
  interest checks on the envelope alone, with no kind names: other
  channels, unrouted kinds (no channel) and events whose routing topics do
  not include a topic page's topic are skipped before rendering or
  authorization (#261 B6). Explicit
  denies from `org.Authorizer.MayReceive` are skipped after rendering. All skips advance
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
  (the organisation endpoint with
  `want=sidebar,messages,typing&channel=…&topic=…`; an unknown topic, or another channel's, is
  404 before the stream opens). It wants an event whose routing `Topics`
  include its topic. A post's routing topic is its persisted posting-time
  topic, even when the render reads a later topic; a move's are its source
  and destination, independently of the rendered item's current topic. An
  event without routing topics (a legacy post without `topic_id`) falls back
  to the topic from the shared render, before authorization, with no extra
  read per stream.
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
  registry lock). It only wakes streams; they read from their cursor, so
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

## Vanished messages

Nothing deletes a message or moves it to another channel yet (#352). Once
something does, replay can reach an event whose message is gone: the read
reports `conversation.ErrMessageNotFound`, the render fails and the loop stops before the event,
so every reconnect repeats it. The first feature that makes this reachable
implements this rule:

- A `message.posted` whose message has vanished is an obsolete target, not a
  failure: it is skipped and the cursor advances.
- A `messages.moved` with some messages vanished still delivers the
  surviving ones, rendered and routed as usual; one missing ID never drops
  the whole event. With every message vanished, it is skipped and the
  cursor advances.
- Any other read, render, authorization or send error stops the loop as
  above, without advancing past the event.

That feature decides how, and in which package, a vanished target becomes a
normal skip, but not by teaching `realtime.Stream`
`conversation.ErrMessageNotFound`. It also decides whether `conversation.Reader.Many` (an incomplete
batch is `conversation.ErrMessageNotFound`) changes, so that ordinary data inconsistency is not
taken for disappearance. Its tests replay a vanished `message.posted` and a
`messages.moved` with missing and surviving IDs, and check that the
surviving messages end in the right state and later events still arrive.
