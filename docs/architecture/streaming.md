# Streaming

## Server-Sent Events

One SSE connection per latest channel page (M3) or topic page (#304),
carrying named events:
`message`, `messages-moved` and `reset` today; `presence`, `typing`
and `unread` are planned (M4). The browser sends everything else as ordinary POST requests. One
process serves every stream; several server processes are future work (the
hub, the per-account cap and the watermark are per process).

Ordering, replay and vanished messages are in [`replay.md`](replay.md).

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
