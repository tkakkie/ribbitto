# Streaming

## Server-Sent Events *(planned, M3)*

One SSE connection per page, carrying named events (`message`, `presence`,
`typing`, `unread`, `reset`). The browser sends everything else as ordinary
POST requests.

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
    C->>DB: event_log WHERE organization_id = org AND seq > cursor ORDER BY seq
    C->>C: authorize, render, send each event; cursor = last seq read
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
- **The loop** is `realtime.Stream.Run` (#209). Another channel's event, a
  kind it does not deliver and an explicit deny from `authz.MayReceive` are
  skipped and the cursor moves past them. An error from the reader, the
  authorization check itself, the renderer or the sender stops the loop
  with the cursor before that event, so a reconnect resumes there; a failed
  membership lookup is never a deny. Cancellation is checked before every
  event, so an ended session sends nothing more. It drains every batch
  before waiting.
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
- A cursor older than the retained `event_log` gets a `reset` event, and the
  client reloads the view.

### Authorization and revocation

- Every event is authorized for the connection's member **immediately before
  sending**, including events already queued: access may have been lost in
  between. The check itself is `app`'s authorization, reached through the
  `Authorizer` interface that `realtime` defines.
- A stream registers with the hub under its session (#207), then looks the
  session up again, so a sign-out in between still stops it. Deleting a
  session (sign-out, or a sign-in replacing it) cancels that session's
  streams at once; the stream's context ends when the session expires.
  Other sessions of the account stay connected.

### Resource limits

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
  however often another channel's events wake it: proxies keep it open, and a client
  that stopped reading is found out at that write's deadline. The loop
  sends it while waiting on the hub (`Stream.Heartbeat`); a failed one ends
  the stream without moving the cursor. Presence (M4) will wait about 30 s
  after a disconnect before showing a member as offline, so a reload does
  not flicker.
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
