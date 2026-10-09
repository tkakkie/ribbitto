# Streaming

## Server-Sent Events

One SSE connection per latest channel page (M3) or topic page (#304),
carrying named events:
`message`, `messages-moved` and `reset` today. M4 replaces these streams
with one organisation-wide stream per tab ([stream scope](#stream-scope-planned-m4)).
The organisation endpoint now exists beside the per-channel and per-topic
endpoints (#714). Pages still use the per-page endpoints until #715 switches
them and removes those routes; no page opens an additional stream.
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

## Stream scope (planned, M4)

[Decision 30](../decisions/30-one-organisation-wide-stream-per-tab-filtered-by-its-interests.md)
gives every page one stream for its organisation, filtered by the page's
interests; [decision 31](../decisions/31-presence-and-typing-are-current-state-with-a-generation.md)
adds presence and typing as current state ([ephemeral state](realtime.md#ephemeral-state-planned-m4)).

`GET /organizations/{slug}/events` already accepts these parameters. Only
`messages` delivers frames; `sidebar`, `typing` and `presence` are accepted
but deliver nothing yet (#286, #285). The page connections and remaining
stream scope below are still planned.

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
| Members panel | `sidebar,presence` with `channel` |

Older page snapshots already provide a cursor; #715 will put
`data-event-cursor` on those pages and connect their stream. Only `message` and
`messages-moved` frames move `Last-Event-ID`; a sidebar frame is a recount,
sent whole at every connect and after coalesced triggers (decision 30). Typing follows
the message filter: a feed shows typing in any topic of its channel, a topic
view only in that topic.

| Event | `id:` | Where htmx puts it |
|---|---|---|
| `message`, `messages-moved` | the sequence | `#message-items`, as today |
| `sidebar` | none | sidebar entries, out of band by their DOM ids (#284) |
| `presence` | none | each member's indicator, out of band by its DOM id (#287) |
| `typing` | none | the content of the page's typing indicator (#288) |
| `reset` | the cursor | the stream script reloads the page, as today |

The element holding `sse-connect` wraps every live region, because the htmx
SSE extension attaches an `sse-swap` element to its closest ancestor with a
source. A whole region (messages, typing) has its own `sse-swap`. Per-entry
events go to one hidden sink, `sse-swap="sidebar,presence"` with
`hx-swap="none"`, whose payload holds only `hx-swap-oob` elements: the
extension swaps through htmx's own swap, which applies out-of-band elements
even when the main swap is `none`, and an entry not on the page is ignored.
#285 proves this in a browser test. Opening the members panel is a
navigation, so its page renders presence and its token from one state.
