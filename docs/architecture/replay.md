# Replay

How an event stream orders, replays and resumes events, and what it does when a message it names has vanished. The connection, retention and authorization are in [`streaming.md`](streaming.md); resource limits are in [`stream-limits.md`](stream-limits.md).

## Ordering and replay

```mermaid
sequenceDiagram
  participant B as Browser
  participant W as web
  participant DB as PostgreSQL
  participant C as stream writer
  B->>W: GET page
  W->>DB: REPEATABLE READ, READ ONLY: page data + event_seq of this organisation
  W->>DB: members panel: a later snapshot of the listing only, with no cursor
  W->>W: members panel: one presence snapshot with token
  W-->>B: HTML with cursor = event_seq, presence indicators and token
  B->>W: GET /organizations/{slug}/events?after=cursor&want=interests
  W->>C: start
  loop
    opt initial read, full-batch drain or newer uncovered durable level
      C->>DB: one snapshot: replay boundary + event_seq + events after cursor
    end
    C->>C: refresh channel state on connect; classify durable triggers before message filtering
    C->>C: check consumer filter and message interest, render, authorize, send; cursor = last seq read
    C->>C: read current ephemeral owners, filter, authorize, send without id or cursor advance
    C->>C: if caught up, wait on org's durable level, declared generations, heartbeat and context
  end
```

- The initial HTML and the cursor are read in **one `REPEATABLE READ READ
  ONLY` transaction**. Under PostgreSQL's default `READ COMMITTED`, each
  statement sees a new snapshot, so a message committed between reading the
  messages and reading `event_seq` would be missing from the page *and*
  skipped by the stream.
- **No lost wakeups.** The durable part of the combined wait means "block until
  the hub's latest sequence for this organisation is greater than both my cursor
  and the last observed durable level", and that condition and wake channel
  are read from one immutable atomic snapshot
  *before* blocking, so a raise before select still wakes that channel's waiter.
  The hub's
  value is level-triggered: if event 101 is committed after the connection
  read `seq > 100` and found nothing, but before it starts waiting, the hub
  already holds 101, so the wait returns at once and the next read delivers
  it. Wakeups are broadcast, never a buffered per-connection
  signal that could be dropped. The cursor advances to the last sequence
  *read*, including events this connection may not see, so a filtered event
  cannot keep the loop spinning.
- After the initial read and full-batch drain, the stream retains the durable
  level covered by its read, including when the hub is ahead of the log.
  Watch-only, ephemeral-only and recount-deadline wakes perform no event-log
  read without a newer durable level. Coverage uses the level known before
  reading, so a raise during the read still prompts another read.
- **The hub** (`realtime.Hub`, #157): `Hub.Raise` only raises the durable
  level, carrying no event data; lower or equal raises are ignored.
  Registration atomically enforces the process and account caps; only
  unregistering frees a slot. Account or session cancellation ends the
  connection; `context.Cause` tells why.
- **The loop** is `realtime.Stream.Run` (#209). `Subscription` owns the
  interest checks on the envelope alone, with no kind names: other
  channels, unrouted kinds (no channel) and events whose routing topics do
  not include a topic page's topic are skipped before rendering or
  authorization (#261 B6). A deny of another member's event audience
  by `org.Authorizer.MayReceive` is skipped after rendering. All skips advance
  the cursor; a deny of the stream's own member ends it without advancing (#852). `messages.moved` reaches feeds and its source/destination topics
  as `messages-moved`, with one shared batch render and per-connection
  authorization. Other topics skip before either. Feeds replace loaded IDs;
  source topics remove them, destinations insert by `event_seq` within the
  loaded range. The server-rendered history control holds the oldest sequence
  (zero when no older history remains, including an empty page); live changes
  never move it or its `before=` link. Only
  Load older replaces it, so older moved items cannot duplicate or skip history.
  Moves are silent; the notice arrives separately as `message.posted`.
  Posts and notices carry the session-resolved `author_id`. Sidebar interests
  receive a shared `unread` frame when message filtering excludes a post: channel,
  posting-time topic, sequence and author, with no `id:`. Legacy posts without
  author or topic send none. Audience and final bound-member checks still apply.
- Channel-state refreshes preserve cursors ([contract](pins.md#ordering-authorization-and-cost), #891).
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
  nothing more. It drains every batch before waiting on the durable level,
  declared ephemeral generations, heartbeat and context together (#735).
  Ephemeral owner frames follow each batch without moving the cursor;
  presence (#768) and typing (#788, #790) are current-state delivery.
- **Missed raises** (#237). Writers without a notifier (`cmd/seed`, sign-up's
  `member.joined`) and, later, other processes (a future multi-process
  deployment) can commit without raising the hub.
  Without the watermark, caught-up streams would wait for another post.
  It periodically checks committed sequences of active organisations (those
  with at least one registered connection) and raises only those still active.
  It only wakes streams;
  they read from their cursor, so `event_log` stays the only truth, and such
  an event arrives after the next successful check. Failed checks are logged
  and retried; without connections it reads nothing.
- Its cost per post grows with the number of open streams; measured in
  [`stream-cost.md`](stream-cost.md).
- Replay and live delivery go through the same per-connection loop, so they
  cannot interleave out of order. `Last-Event-ID` is preferred on reconnect;
  before htmx recreates the `EventSource`, the client puts its transport cursor
  into `after`. DOM updates are idempotent (elements are replaced by id), so
  a duplicate delivery is harmless.
- Completed unread-only or skipped progress beyond the last sent ID produces
  `event: checkpoint`, `id: <cursor>` and empty `data:` at most once per
  heartbeat interval, measured from the last checkpoint or ID-bearing write.
  Pending progress replaces a due heartbeat comment; with heartbeats disabled,
  it is checkpointed before waiting.
  A fresh organisation/bound-member check immediately precedes each send.
  No checkpoint covers a failed render, authorization or send. The browser
  updates only `sse-connect`'s `after`; applied/shown cursors still advance
  only through message/move application and the visibility guard. Before a
  checkpoint is received, disconnects may replay about one heartbeat interval
  of events as duplicates.
- A cursor below `organization.event_log_boundary_seq` or above the committed
  `organization.event_seq` gets one `reset` and the loop returns without
  advancing it or sending later events. The upper check covers cursors ahead
  of the log after a stopped restore
  ([Restoring a backup](../../README.md#restoring-a-backup)); rolling the
  database back under a running process is unsupported. Every batch, including
  on open streams and cache hits, checks both inclusive bounds, even on an
  empty log; at `event_seq` it waits. Any gap resets before delivering the batch.
  The typed `message-stream` bundle closes htmx's source on `reset`, then reloads after
  0–5 s jitter when no send, queue or failed text is at stake
  ([guard](web-javascript.md#javascript)). A status offers a reload link while
  held; confirmed items do not hold it.

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
