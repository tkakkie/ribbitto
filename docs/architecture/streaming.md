# Streaming

## Server-Sent Events

One SSE connection per latest channel page (M3) or topic page (#304),
carrying named events:
`message`, `messages-moved` and `reset` today; `presence`, `typing`
and `unread` are planned (M4). The browser sends everything else as ordinary POST requests. One
process serves every stream; several server processes are future work (the
hub, both stream caps and the watermark are per process).

Ordering, replay and vanished messages are in [`replay.md`](replay.md).
A shared reader per organisation, which would replace each connection's
own reads, is designed but not built (#232): [`shared-reader.md`](shared-reader.md).

### Retention

`realtime.Retention` expires replay history once at start and then hourly.
The worker starts asynchronously before serving, so its first run can overlap
requests. `realtime`'s store runs it (`realtimepg.NewCleaner`); org's lock and boundary
come through the injected `realtime.RetentionBoundary` (`orgpg.RetentionBoundaryIn`,
org's store). It takes the same organisation lock as posting; separate transactions
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
  the database (#262). `org.Authorizer.MayReceive` first reads a fresh
  access epoch, sharing reads only with overlapping checks and retaining no
  epoch values. A bounded allow cache keyed by (account, slug) may supply the
  membership only when its epoch is at least that fresh epoch; otherwise an
  independent membership query reads the membership and epoch together.
  A later check prevents an older in-flight result from entering the cache.
  The installation-wide sequence and database triggers invalidate allows
  across direct SQL revocations, renames and organisation recreation (#669).
  Missing rows deny; read errors stop the stream. Denies are not cached;
  they skip the event and advance its cursor. Organisation and audience
  checks still apply to every event. A denied render is discarded.
  Only the write remains between the check and the connection; a revocation
  after the epoch snapshot can still let that event through. This implements
  [option (a2)](stream-authorization.md#decision) (#670).
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

Cache-load limits, write deadlines, heartbeats, the process and per-account stream caps,
shutdown and HTTP/2 are in [`stream-limits.md`](stream-limits.md).
