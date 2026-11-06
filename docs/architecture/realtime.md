# Real time

## Durable event log

`event_log` is keyed by `(organization_id, seq)`, with `kind`, nullable
`audience_member_id`, IDs-only JSONB `data` and `created_at`. A NULL audience
is organisation-wide; a value restricts delivery to that member, enforced
by the stream's per-event authorization. Its composite foreign key keeps the
member in the same organisation. The audience never appears in `data`.
Each kind's publisher owns its kind name and payload (decision 26):
realtime keeps only the `EventKind` type. Payload IDs are canonical UUID text,
so old and new rows decode alike.
`message.posted` carries `{"channel_id","message_id","topic_id","author_id"}`
(UUIDs); the topic and author are captured at posting time, in the message's
transaction, including branch notices. The posting-time topic is its routing topic;
old rows without the field have none, while a present malformed value fails
the batch. A later move never
rewrites this routing data: replay applies the posting and move in order.
`member.joined` carries `{"member_id":"<uuid>"}`; `messages.moved` (branching,
[topics](../domain/topics.md#branching)) carries the channel, the two topics
and the moved message IDs. All use a NULL audience.
A move routes to its channel and both topics. Its decoder rejects missing or
malformed IDs, identical source and destination,
and empty lists or repeated messages, failing the whole batch rather than
returning a partial replay. The size limit applies only on write, so lowering
it cannot make committed moves unreadable. Moves read the requested IDs,
authors and current topics in one shared snapshot, bounded
by the event's ID list (at most 100 on write). One cached render per
organisation, channel, move sequence and language serves feeds and topics;
posting-cache entries cannot mask the correction. Authorization stays per
connection, after rendering and immediately before send. The payload includes
source and destination IDs and each item's original sequence: feeds replace
loaded IDs; topics remove source items or insert destination items in order
within the loaded range. Live changes never alter the history paging bound.
`web`'s renderer chooses by kind: `message.posted` and `messages.moved`
each have their own render, and any other kind that reaches it is an error
naming the kind, which stops the stream before that event instead of
rendering it as a post. A render depends only on the event.
For `sidebar` streams whose message interest does not select a post,
the cached `unread` frame is language-independent and comes from `message.posted`'s
payload without a message read.
Setup and sign-up append `member.joined` immediately after the member,
with its `joined_event_seq`, in the same transaction owned by the root use case.
Setup and sign-up do not raise the hub;
the watermark covers it. Each publisher registers a `Router` for its kinds;
wiring combines the registrations. Kinds are an open list; a duplicate registration
fails start-up with the kind named, rather than replacing its router.
`EventsAfter` returns
organisation-scoped rows with `seq > after`, in sequence order, at most `limit`.
It routes registered kinds through their `Router`s, failing the batch for malformed or missing IDs;
unregistered kinds keep only their envelope, with no channel, so streams skip them.
Authorization remains the connection loop's job; `org` owns cursor bounds.

`organization.event_log_boundary_seq` is the highest sequence no longer in
the log. Migration sets it to each existing organisation's `event_seq`,
without backfilling; new organisations start at 0. Rows above the boundary
are gap-free through `event_seq`; a deferred constraint trigger enforces it at
commit for every writer, including an older binary still running during
`migrate up`. Retention raises the boundary in the same transaction as deletion,
locking only one organisation per transaction with `FOR NO KEY UPDATE` before its events.
This serialises batches with `event_seq` writers
while allowing foreign-key inserts into child tables. The boundary must never
regress. Retention lists organisations with expired rows in ID order without write locks,
then commits batches of at most 1,000 expired
rows in sequence order until no eligible prefix rows remain for each organisation.
Eligible rows are the expired prefix:
those below the organisation's lowest-sequence row that has not expired, or
every expired row when none has, since `created_at` (the writing transaction's
start) need not follow `seq` (#430); expired rows above that row wait for a
later run. Each batch reads only the organisation's 1,000 lowest sequences
under the lock and deletes the expired run at their start, so the work under
the lock stays bounded. Each batch reads after acquiring the organisation lock,
so concurrent cleaners see committed progress. Every update of that organisation's
row still waits, including sequence writes, access-epoch changes and another
cleaner's boundary raise. Messages, their sequences and read state are untouched.
Worker timing, retries and shutdown: [retention lifecycle](streaming.md#retention).
A cursor is valid from the boundary through the committed `event_seq`,
inclusive, even with an empty log; at `event_seq` it waits for new events.
`EventsAfter` reads both bounds, then the rows, in one read-only snapshot, returning
`realtime.ErrCursorExpired` outside those bounds, including with a zero limit. Every full
`CachedEvents` result gets a fresh zero-limit check: immutable cached rows and
both bounds describe a valid batch at the check's snapshot, or require reset.
Short batches already carry their read's checks of both bounds. Cached rows
are assumed immutable: a restore happens with ribbitto stopped, so caches and
the hub start empty ([Restoring a backup](../../README.md#restoring-a-backup)).

## Ephemeral state

Presence and typing travel as current state with a generation, not as events:
[ephemeral state](ephemeral-state.md). Production typing replaces a stable,
polite status region on latest feeds/topics without changing durable cursors.
