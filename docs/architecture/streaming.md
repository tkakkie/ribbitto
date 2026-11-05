# Streaming

## Server-Sent Events

One organisation-wide SSE connection per tab, including older history pages,
filtered by [interests](stream-scope.md#stream-scope).
Named events are `message`, `messages-moved`, `checkpoint` and durable
`reset` with `id:`, plus `unread`, `sidebar`, `presence`, `typing`, `pins` and owner
`reset` without `id:`.
Other browser actions use POST. One process serves every stream; multiple
processes are future work (hub, stream caps and watermark are per process).

Ordering, replay and vanished messages are in [`replay.md`](replay.md).
[Channel-state refreshes](pins.md#ordering-authorization-and-cost) use
consumer-defined frames without SSE IDs.

### Retention

Retention expires replay history once at start and then hourly.
The worker starts asynchronously before serving, so its first run can overlap
requests. Each run has a one-minute timeout; the
[event-log contract](realtime.md#durable-event-log) bounds deletion batches and
defines the boundary, lock order and cursor validity.
Failed runs retry on the next tick, keeping committed progress. Shutdown cancels
and waits for the worker, including a blocked first run, before closing the pool.
The retention period defaults to seven days; see [database configuration](../database.md).

### Authorization and revocation

- Before registration, `Sec-Fetch-Site` must be absent or a single
  `same-origin` value; otherwise the handler returns 403 without a slot.
  This supplements `http.CrossOriginProtection`, which exempts GET.
- Every event is authorized for the connection's member **after it is
  rendered and immediately before sending**, including events already
  queued: access may have been lost in between, including while rendering (#262).
  An access removal committed before an event's check starts prevents delivery;
  one committed after the check starts may still let that event through.
  Authorization first reads a fresh
  access epoch, sharing reads only with overlapping checks and retaining no
  epoch values. A bounded allow cache keyed by (account, slug) may supply the
  membership only when its epoch is at least that fresh epoch; otherwise an
  independent membership query reads the membership and epoch together.
  A later check prevents an older in-flight result from entering the cache.
  The installation-wide sequence and database triggers invalidate allows
  across direct SQL revocations, renames and organisation recreation (#669).
  Missing rows deny; read errors stop the stream. Durable, sidebar, presence
  and typing delivery also check `AudienceMemberID == sub.Member`.
  A bound-member deny ends `Run` with `ErrMembershipRevoked`; another
  member's event audience still only skips and advances the cursor.
  Heartbeats re-check membership, ending idle revocation within one interval.
  Removal reconnects get 404; replacement reconnects bind the new member.
  Cleanup releases old presence and typing counts; other streams continue.
  Denies are not cached; denied renders are discarded.
  Only the write remains between the check and the connection; a revocation
  after the epoch snapshot can still let that event through. This implements
  [option (a2)](stream-authorization.md#decision) (#670).
- A stream registers with the hub under its session (#207), then looks the
  session up again, so a sign-out in between still stops it. Deleting a
  session (sign-out, or a sign-in replacing it) cancels that session's
  streams at once; the stream's context ends when the session expires.
  Other sessions of the account stay connected for single-session sign-out.
  [Signing out of other sessions](identity.md) ends those streams instead;
  a mutation error cancels the whole account so reconnects re-check sessions.
  Failed registrations free their slots; each accepted stream counts once,
  whatever its interests, and cleanup releases its slot and presence/typing counts.
  Typing uses separate channel counts: the last matching close clears activity
  immediately ([typing lifetime](typing.md)); failed opens take no count. Presence stays online
  until 30 s after the last close; a reconnect cancels expiry.
  Before HTTP commitment, cancellation answers 404 for an ended session or 503
  for shutdown. `WriteHeader` commits after a final context check, outside the
  sender whose cancellation deadline resets HTTP/2. Later cancellation ends
  the started stream without changing status (decision on #315).

### Resource limits

Cache loads, write deadlines, heartbeats, stream caps, shutdown and HTTP/2: [`stream-limits.md`](stream-limits.md).

## Stream scope

See [stream scope](stream-scope.md).
