# Real time

How a message is posted, with the organisation's sequence taken first: [posting](posting.md).

## Durable event log

`event_log` is keyed by `(organization_id, seq)`, with `kind`, nullable
`audience_member_id`, IDs-only JSONB `data` and `created_at`. A NULL audience
is organisation-wide; a value restricts delivery to that member, enforced
by the stream's per-event authorization (`org.Authorizer.MayReceive`). Its composite foreign key keeps the
member in the same organisation. The audience never appears in `data`.
Each kind's publisher owns its kind name and payload (decision 26):
`conversation` (`KindPosted`, `EncodePosted`, `DecodePosted`;
`KindMessagesMoved`, `EncodeMoved`, `DecodeMoved`) and `org` (`KindJoined`, `EncodeJoined`, `DecodeJoined`), declare the
kind, encode the payload and decode it; realtime keeps only the
`EventKind` type. The encoders return only `[]byte`: their strings and
string slices cannot fail JSON marshaling. The writer stores what they
return; the reader only routes through their `Router`s, and consumers
decode the payload (the renderer decodes moves). IDs are canonical UUID text
(`realtime.FormatPayloadID`, `ParsePayloadID`), the JSON value SQL's
`jsonb_build_object` wrote before (#398), so old and new rows decode alike.
`message.posted` carries `{"channel_id","message_id","topic_id"}` (UUIDs);
the topic is captured at posting time, in the message's transaction, including
branch notices. `conversation.RoutePosted` gives it as the event's routing topic;
old rows without the field have none, while a present malformed value fails
the batch. A later move never
rewrites this routing data: replay applies the posting and move in order.
`member.joined` carries `{"member_id":"<uuid>"}`; `messages.moved` (branching,
[topics](../domain/topics.md#branching)) carries the channel, the two topics
and the moved message IDs. All use a NULL audience.
`conversation.RouteMoved` routes a move to its channel and both topics; the
renderer decodes the payload through `conversation.DecodeMoved`. The decoder rejects missing or malformed IDs, identical source and destination,
and empty lists or repeated messages, failing the whole batch rather than
returning a partial replay. The size limit applies only on write, so lowering
it cannot make committed moves unreadable. Moves read the requested IDs,
authors and current topics in one shared `conversation.Reader.Many` snapshot, bounded
by the event's ID list (currently at most 100 on write). One cached render per
organisation, channel, move sequence and language serves feeds and topics;
posting-cache entries cannot mask the correction. Authorization stays per
connection, after rendering and immediately before send. The payload includes
source and destination IDs and each item's original sequence: feeds replace
loaded IDs; topics remove source items or insert destination items in order
within the loaded range. Live changes never alter the history paging bound.
`web`'s renderer chooses by kind: `message.posted` and `messages.moved`
each have their own render, and any other kind that reaches it is an error
naming the kind, which stops the stream before that event instead of
rendering it as a post. A render depends only on the event
(`Renderer.Render(ctx, event)`); the stream has already applied the
subscription, and the renderer reads only `MessageReader`'s `One` and `Many`.
Setup and sign-up append `member.joined` (`org.EncodeJoined`) the same way immediately after
the member, with its `joined_event_seq`. Their root use cases own that
transaction and inject realtime's writer through `org.EventAppenderIn`.
Setup and sign-up do not raise the hub;
the watermark covers it. `realtime.Event` is an envelope: organisation,
sequence, kind, audience, channel, routing `Topics` and the stored `Payload`,
which consumers decode through the publisher's codec; kinds are an open list.
Each publisher registers its `Router` in `realtime.Kinds`, which gives
the channel and routing topics. `orgpg.EventKinds()` provides `org.RouteJoined`
and `conversationpg.EventKinds()` provides `conversation.RoutePosted` and
`conversation.RouteMoved`; wiring in `cmd/ribbitto` and the tests merges both
registries through `realtime.MergeKinds`, which rejects a duplicate kind
instead of replacing its router. A duplicate fails start-up with the kind named.
`realtimepg.NewReader(pool, bounds, kinds)` provides
`EventsAfter(ctx, organizationID, after, limit) ([]realtime.Event, error)`:
organisation-scoped rows with `seq > after`, in sequence order, at most `limit`.
It routes registered kinds through their `Router`s, failing the batch for malformed or missing IDs;
unregistered kinds keep only their envelope, with no channel, so streams skip them.
The reader is `realtime`'s store (`internal/realtime/internal/postgres`, on
its own sqlc entry `db/queries/realtime/`) and reads only `event_log`. org's
cursor bounds come through the injected `realtime.BoundsIn` (`orgpg.BoundsIn`,
and the watermark's `orgpg.NewSequences`, over org's store and its sqlc entry
`db/queries/org/`); authorization remains the connection loop's job.

`organization.event_log_boundary_seq` is the highest sequence no longer in
the log. Migration sets it to each existing organisation's `event_seq`,
without backfilling; new organisations start at 0. Rows above the boundary
are gap-free through `event_seq`; a deferred constraint trigger enforces it at
commit for every writer, including an older binary still running during
`migrate up`. Retention raises the boundary in the same transaction as deletion,
locking one organisation before its events as [posting](posting.md) does; the lock and the
boundary (`greatest`, so it never goes down) come through the injected
`realtime.RetentionBoundary`. The cleaner lists organisations with expired rows
from `event_log` in ID order without write locks, then commits
batches of at most 1,000 expired rows in sequence order until no eligible
prefix rows remain for each organisation. Eligible rows are the expired prefix:
those below the organisation's lowest-sequence row that has not expired, or
every expired row when none has, since `created_at` (the writing transaction's
start) need not follow `seq` (#430); expired rows above that row wait for a
later run. Each batch reads only the organisation's 1,000 lowest sequences
under the lock and deletes the expired run at their start, so the work under
the lock stays bounded. Each batch reads after acquiring the organisation lock,
so concurrent cleaners see committed progress. Only that organisation's writers
wait; errors or the one-minute run timeout preserve all committed batches for
the next hourly tick. Messages,
their sequences and unread positions are untouched.
A cursor is valid from the boundary through the committed `event_seq`,
inclusive, even with an empty log; at `event_seq` it waits for new events.
`EventsAfter` reads both bounds, then the rows, in one read-only snapshot, returning
`realtime.ErrCursorExpired` outside those bounds, including with a zero limit. Every full
`CachedEvents` result gets a fresh zero-limit check: immutable cached rows and
both bounds describe a valid batch at the check's snapshot, or require reset.
Short batches already carry their read's checks of both bounds. Cached rows
are assumed immutable: a restore happens with ribbitto stopped, so caches and
the hub start empty ([Restoring a backup](../../README.md#restoring-a-backup)).

## Ephemeral state (planned, M4)

[Decision 31](../decisions/31-presence-and-typing-are-current-state-with-a-generation.md);
which connections ask for it is [stream scope](streaming.md#stream-scope-planned-m4).

- **Owners.** Presence (#287) knows which members have a stream open in the
  organisation, from the hub's registrations, and marks a member offline
  about 30 s after their last stream closes. Typing (#288) knows who is
  typing in which channel and topic, until a few seconds after their last
  signal. `realtime` defines the interface the owners implement, as it does
  for `Renderer` and `Authorizer`; `cmd/ribbitto` wires them.
- **Generations.** Each presence entry records the generation of its last
  change, and entries are kept in that order, so the changes after a
  generation are a suffix. An entry that went offline stays for a bounded
  time so the change can still be sent; dropping it raises the
  organisation's **discard boundary**, a monotonic level. The owner updates
  state, boundary and generation together and then raises the hub's
  generation; a read returns all three as of one moment, so a connection
  marks seen only what it read. Typing keeps a summary per channel and per
  topic, updated with each change: how many are typing, the four latest
  typists and the generation of its last change. A connection reads only
  its place's summary (a feed reads its channel's) and drops the viewer
  from it in constant time, so its work does not grow with typists or
  topics elsewhere.
- **Levels.** Per organisation the hub holds the durable level and one
  generation per kind (`presence`, `typing`), each published as an atomic
  level and channel like the durable level ([stream limits](stream-limits.md)).
  Only visible changes raise a generation: going online or offline,
  starting or stopping typing. Typing expiry is a change too, raised by its
  owner's own timer, not by clients.
- **Delivery.** The writer waits on the durable level, the generations of
  its interests, its heartbeat and its context together. After each
  durable batch, and before waiting, it sends one frame without `id:` per
  kind whose generation passed what it has seen: the presence entries
  changed since then, read from the suffix and stopping past the frame's
  limit (100 entries), or its place's typing indicator (three names at
  most, then a count) when its summary changed. Presence entries render
  once per member, state and language and are shared.
- **Reset.** A presence read whose start lies below the discard boundary,
  from another process's token, or past the frame's limit sends `reset`
  instead (decision 24), on connect or later; the page reloads and renders
  presence with a fresh token.
- **Authorization** runs immediately before each frame, by the rule for an
  organisation-wide event (presence) or one of the frame's channel (typing):
  a deny skips the frame and its generation counts as seen, and a failed
  check stops the stream, as for durable events. Checks per connection are
  bounded by batches and wakes, at most one per kind each, not by changes;
  raises are bounded by visible transitions, and typing signals by a
  per-member limit (#288).
- **Restore.** At connect, typing sends its current indicator, presence the
  changes after the page's token, and the sidebar a recount (decision 30).
- **One process.** State and levels are per process; several processes need
  a shared source for both (#236).
