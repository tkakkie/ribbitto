# Streaming

## Server-Sent Events

One organisation-wide SSE connection per tab, including directly opened
older history pages, filtered by the page's [interests](#stream-scope).
Named durable events are `message`, `messages-moved` and `reset`; wired
ephemeral owners can also supply `presence` and `typing` without `id:`.
The browser sends everything else as ordinary POST requests. One
process serves every stream; several server processes are future work (the
hub, both stream caps and the watermark are per process).

Ordering, replay and vanished messages are in [`replay.md`](replay.md).
A shared reader per organisation, which would serve caught-up connections
from a window instead of their own reads (replay still reads the database),
is designed but not built (#232): [`shared-reader.md`](shared-reader.md).

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

- Before registration, `Sec-Fetch-Site` must be absent or a single
  `same-origin` value; otherwise the handler returns 403 without a slot.
  This supplements `http.CrossOriginProtection`, which exempts GET.
- Every event is authorized for the connection's member **after it is
  rendered and immediately before sending**, including events already
  queued: access may have been lost in between, including while rendering (#262).
  `org.Authorizer.MayReceive` first reads a fresh
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
  Other sessions of the account stay connected. The handler's private
  `openStream` helper owns registration, the re-check and the expiry deadline.
  It frees failed registrations; on success, the handler defers one cleanup
  function that cancels the expiry context, unregisters the stream and
  releases its presence and channel typing counts. Only a successful registration and session
  re-check count, through `Streaming.StreamOpened` with the resolved
  organisation, member and channel; every interest counts.
  Typing uses separate channel counts: the last matching close clears activity
  immediately ([typing lifetime](typing.md)); failed opens take no count. Presence stays online
  until 30 s after the last close; a reconnect cancels expiry.
  Cancellation before the first write answers 404 for an ended session or
  503 for shutdown, without committing SSE headers. The status is
  committed by `WriteHeader` outside the sender, after one more look at the
  context: the sender expires the write deadline on cancellation, which
  over HTTP/2 resets the stream, so the answer would never arrive. A
  cancellation after `WriteHeader` (a window no lock can close) ends a
  stream that has started, without trying to change its HTTP status
  (maintainer's decision on #315).

### Resource limits

[Recount scheduler](recount-scheduler.md).

Cache-load limits, write deadlines, heartbeats, the process and per-account stream caps,
shutdown and HTTP/2 are in [`stream-limits.md`](stream-limits.md).

## Stream scope

[Decision 30](../decisions/30-one-organisation-wide-stream-per-tab-filtered-by-its-interests.md)
gives every page one stream for its organisation, filtered by the page's
interests; [decision 31](../decisions/31-presence-and-typing-are-current-state-with-a-generation.md)
delivers presence and typing as current state ([ephemeral state](ephemeral-state.md#ephemeral-state)).

`GET /organizations/{slug}/events` accepts these parameters. `messages`
delivers durable frames; `typing` and `presence` read their wired
owners after each durable batch and before waiting, at most one frame per
kind. Presence checks the organisation, typing
the frame's channel, immediately before sending. Bound-member denies and errors stop the stream. Web's presence (#768) and viewer-filtered typing (#788) adapters are wired in production. Sidebar recounts are wired in production (#819); composition details are in [unread counts](unread-counts.md).

The endpoint takes:

| Parameter | Meaning |
|---|---|
| `after` | the durable cursor; `Last-Event-ID` wins |
| `want` | comma-separated interests, each at most once: `sidebar`, `messages`, `typing`, `presence`; anything else is 400 |
| `channel` | the page's channel: required with `messages`, `typing` or `sidebar` (for its initial recount); with `sidebar` it selects whose topics the sidebar counts; a channel the member cannot see is 404 |
| `topic` | optional with `messages`, `typing` or `sidebar`; requires `channel`, narrows messages/typing and retains sidebar selection; another organisation or channel is 404 |
| `panel` | optional `members`; ignored since #874 |
| `presence-after` | required with `presence`: the process instance and presence generation the page was rendered with |

| Page | `want` |
|---|---|
| Feed (latest page) | `sidebar,messages,typing` with `channel` |
| Topic view (latest page) | `sidebar,messages,typing` with `channel` and `topic` |
| `?before=` page | `sidebar` with `channel` and, on topic pages, `topic` |
| With the members panel | the page's own interests plus `presence`, with `presence-after` |

Every page carries its snapshot cursor on `#organization-stream`, including
`?before=` pages, which hold a slot under both stream caps. Only `message`
and `messages-moved` frames advance the browser's durable cursor.

Ephemeral delivery filters organisation, interest and typing channel/topic
before authorization, preserving the durable cursor and heartbeat deadlines.
Typing receives server-resolved member identity and after=-1 on connect,
restoring current content, even empty, in `#typing-status`. Later reads acknowledge the organisation level
without repeating unchanged places ([owner contract](typing.md#production-delivery)).
Presence receives the page
token: one coalesced entry per member, in last-change order, at most 100.
Offline entries stay five minutes; discarding one advances a monotonic boundary.
The state, boundary and generation come from one locked read. Another process's
token, a start below the boundary or over 100 changes require `reset` on connect
or later; equality at the boundary is served. The presence adapter sends
that reset without `id:`, ending the stream so the page reloads.
Sidebars recount on connect, posts/moves and the member's read advances,
regardless of message interests. With a hub, Reading and watches share
one `unread.Changes`; otherwise the publisher is nil. Subscribe before recounting;
release on exit. Capture generations before each snapshot; send or deny covers
only that generation. Changes dirty a [fixed deadline](stream-limits.md) checked
between batches and in the combined wait. Authorize organisation and bound member
after rendering, before sending. Frames omit `id:`; errors stop delivery. Reconnect recounts.
[Short results](read-advance-results.md) record costs and acceptance. The
[single-process contract](ephemeral-state.md#member-read-change-watches) and
[unknown-COMMIT exception](unread-writes.md) limit recovery.
Swap targets:

| Event | `id:` | Where htmx puts it |
|---|---|---|
| `message`, `messages-moved` | the sequence | `#message-items` |
| `sidebar` | none | whole sidebar lists and selected heading count, out of band (#818) |
| `presence` | none | each member's indicator, out of band by its DOM id (#768) |
| `typing` | none | `#typing-status` content replacement on latest feeds/topics (#790) |
| `reset` | the cursor for durable reset; none for owner reset | the stream script reloads the page |

`#organization-stream`, holding `sse-connect`, wraps the sidebar, conversation and any side panel, because the htmx
SSE extension attaches an `sse-swap` element to its closest ancestor with a
source. Messages have their own `sse-swap` on latest pages; older pages omit it.
Typing replaces content outside the composer. Per-entry events go to the hidden
`#stream-sink` inside this container, `sse-swap="sidebar,presence"` with
`hx-swap="none"`, whose payload holds only `hx-swap-oob` elements: the
extension swaps through htmx's own swap, which applies out-of-band elements
even when the main swap is `none`, and an entry not on the page is ignored.
The sink is hidden from assistive technology and takes no focus.
`TestMessageStreamBrowser` proves replacement and missing-target behavior with
test-only entries rendered with `html/template`, and that native `Last-Event-ID` and the page's
reconnect cursor stay unchanged after ephemeral frames. Production presence and sidebar frames share the page components (#768, #818, #819). Opening, switching or closing a
panel is a navigation. Its bounded, paged list has no live list updates. One
presence snapshot supplies every indicator and the page token; changes replace
only loaded indicators, with visible localized labels and the success dot.
