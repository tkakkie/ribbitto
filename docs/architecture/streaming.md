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
  function that cancels the expiry context, unregisters the stream and
  releases its presence count. Only a successful registration and session
  re-check count, through `Streaming.StreamOpened` with the resolved
  organisation and member; every interest counts. Presence stays online
  until 30 s after the last close; a reconnect cancels expiry. State, bounded
  snapshots (#766) and changes/reset (#767) are current; rendering/delivery
  (#768) remain planned.
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

## Stream scope

[Decision 30](../decisions/30-one-organisation-wide-stream-per-tab-filtered-by-its-interests.md)
gives every page one stream for its organisation, filtered by the page's
interests; [decision 31](../decisions/31-presence-and-typing-are-current-state-with-a-generation.md)
delivers presence and typing as current state ([ephemeral state](realtime.md#ephemeral-state)).

`GET /organizations/{slug}/events` accepts these parameters. `messages`
delivers durable frames; `typing` and `presence` read their wired
owners after each durable batch and before waiting, at most one frame and
one authorization check per kind. Presence checks the organisation, typing
the frame's channel, immediately before sending. Denies mark the generation
seen; errors stop the stream. Owner adapters remain planned (#768, #288), so no
ephemeral frames are sent in production yet. `sidebar` remains planned (#286).

The endpoint takes:

| Parameter | Meaning |
|---|---|
| `after` | the durable cursor; `Last-Event-ID` wins, as today |
| `want` | comma-separated interests, each at most once: `sidebar`, `messages`, `typing`, `presence`; anything else is 400 |
| `channel` | the page's channel: required with `messages` or `typing`; with `sidebar` it selects whose topics the sidebar counts; a channel the member cannot see is 404 |
| `topic` | optional with `messages` or `typing`, and 400 without them: narrows both to one topic; a topic of another channel is 404 |
| `presence-after` | required with `presence`: the process instance and presence generation the page was rendered with |

| Page | `want` |
|---|---|
| Feed (latest page) | `sidebar,messages,typing` with `channel` |
| Topic view (latest page) | `sidebar,messages,typing` with `channel` and `topic` |
| `?before=` page | `sidebar` with `channel` |
| Members panel | `sidebar` with `channel`; #768 adds `presence` and its token |

Every page carries its snapshot cursor on `#organization-stream`, including
`?before=` pages, which hold a slot under both stream caps. Only `message`
and `messages-moved` frames advance the browser's durable cursor.

Ephemeral delivery filters organisation, interest and typing channel/topic
before authorization, preserving the durable cursor and heartbeat deadlines.
Owners decide connect output from current state; presence receives the page
token: one coalesced entry per member, in last-change order, at most 100.
Offline entries stay five minutes; discarding one advances a monotonic boundary.
The state, boundary and generation come from one locked read. Another process's
token, a start below the boundary or over 100 changes require `reset` on connect
or later; equality at the boundary is served. The planned adapter (#768) sends
that reset without `id:`, ending the stream so the page reloads.
Sidebar frames remain planned (#286), recounting at connect and after
coalesced triggers. Their swap targets are listed below; feature entries and
the typing region remain planned.

| Event | `id:` | Where htmx puts it |
|---|---|---|
| `message`, `messages-moved` | the sequence | `#message-items`, as today |
| `sidebar` | none | sidebar entries, out of band by their DOM ids (#284) |
| `presence` | none | each member's indicator, out of band by its DOM id (#768) |
| `typing` | none | the content of the page's typing indicator (#288) |
| `reset` | the cursor for durable reset; none for owner reset | the stream script reloads the page, as today |

`#organization-stream`, holding `sse-connect`, wraps the sidebar and conversation or members panel, because the htmx
SSE extension attaches an `sse-swap` element to its closest ancestor with a
source. Messages have their own `sse-swap` on latest pages; older pages omit it.
Planned typing has its own region. Per-entry events go to the hidden
`#stream-sink` inside this container, `sse-swap="sidebar,presence"` with
`hx-swap="none"`, whose payload holds only `hx-swap-oob` elements: the
extension swaps through htmx's own swap, which applies out-of-band elements
even when the main swap is `none`, and an entry not on the page is ignored.
The sink is hidden from assistive technology and takes no focus.
`TestMessageStreamBrowser` proves replacement and missing-target behavior with
test-only entries rendered with `html/template`, and that native `Last-Event-ID` and the page's
reconnect cursor stay unchanged after ephemeral frames. Production frames
remain planned (#286, #768). Opening the members panel is a
navigation. It lists members without live list updates; #768 will render presence
and its token from one state.
