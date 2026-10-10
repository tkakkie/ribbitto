# 32. Read state is a set of read ranges per member and channel

**Decided** (#282; supersedes nothing; settles the unread model that
[decision 21](21-topics-inside-channels-a-default-topic-and-branching-instead-of-threads.md)
left to M4). Storage, single-range unions, the feed write and branch-notice
reads are implemented;
HTTP callers, other reading writes and counts remain *planned* for M4. Rules in [unread](../domain/unread.md),
storage and cost in [unread counts](../architecture/unread-counts.md), writes in
[unread writes](../architecture/unread-writes.md).

- **Read ranges.** A member's read state in a channel is a set of
  `message.event_seq` ranges, one row each (`read_range`), starting with
  everything up to `joined_event_seq`. A message is read when its sequence
  is in the set. Writes add and merge ranges under a per-member, per-channel
  lock, so the set only grows. Every gap between two ranges holds an
  unread message of the channel.
- **Two reading scopes.** The feed reads every message of the channel up to
  the cursor the page has applied and shown; a topic view reads only that
  topic's messages, except any moved in after that cursor. Only POSTs
  advance read state: the first is bounded by the page's snapshot, later
  ones (#710) by what the page has applied and shown. A message the stream
  has only received, or the sequence in a post's own response, never makes
  an unshown message read. Without JavaScript, a *Mark as read* form sends
  the same POST. Posting reads its page's scope up to the same cursor, and a
  member's own posts are read for them. Branching currently adds its author's
  notice to the read set in the branching transaction through an injected writer.
- **Counts.** A channel's count is all its unread messages, the sum over all
  its topics; a topic's is those in that topic. Each list (the sidebar's
  channels, the current channel's topics) takes two statements in the
  page's snapshot, one in `unread` and one in `conversation`, whatever its
  length and never one per item; counts are capped at 100 each. Channel counts probe the set's first gaps; topic
  counts start at a per-topic floor (`topic_read_floor`) and look up
  messages moved in since (`message.moved_event_seq`). Nothing reads
  `event_log`.
- **The divider** is the scope's first unread message as the page rendered
  it, carried by the page for the whole visit; nothing stores a previous
  position.
- **Ownership.** Following decision 27's growth rule, an `unread` module
  owns the read-state tables and reads messages only through
  `conversation`'s transaction-bound write queries and planned snapshot-bound
  counting API; `conversation` owns
  `message.moved_event_seq`, which extends its messages. The page snapshot
  reaches `unread` through an injected factory.

**Why:** a set of sequences cannot be changed by a move, so both topic tests
hold by construction: branching keeps `event_seq`, and a topic view adds
only its own messages. A moved unread message stays unread whatever the
destination's state, with no write per member when messages move. With
gaps that always hold an unread message and a cap of 100, a channel count
reads at most 101 ranges and 100 messages. A topic count reads its unread
messages up to 100 plus read rows above its floor; those are few while the
stream keeps the page current, but grow without a bound with own posts made
while it lags or is disconnected, so #283 benchmarks them, and topic counts
switch to probing gaps before implementation if the cost is unacceptable
(maintainer's directions, 2026-10-09: read ranges first; fixed statements
per list).

**Considered:**
- *A per-channel position plus per-topic positions* (a message is read if
  it is at or below either): a read message moved into a topic with a lower
  position becomes unread, and an unread one moved into a topic read further
  becomes read. Fixing both needs a write for every member at every move,
  under the organisation's lock.
- *A per-topic position that never drops below a message's own read state*:
  keeping it needs each message's read state per member at move time, the
  same per-member write.
- *A read row per member and message*: exact and move-safe, but storage and
  writes grow with members times messages (a feed read of 1,000 unread
  messages writes 1,000 rows), and counts become anti-joins.

Accepted costs: when a member reads only topic views while another topic
keeps growing, the set gains a row per skipped run; channel counts still
read only the first 101 ranges, but topic counts, topic-view writes and the
feed read that merges them back handle all of them (`O(R)`). Topic counts
also read the member's own posts made while the stream lagged, without a
bound. #283 benchmarks both (and stream delay or disconnection, and 10,000
ranges) before implementation starts; the maintainer decided go on
2026-10-10 ([benchmark results](../architecture/unread-benchmark-results.md#decision)),
not as a performance guarantee. One row per range, rather than an
`int8multirange` column, keeps channel counts to the first gaps; the
statements' query-gate additions are reviewed separately in #740–#743 and
#749: UUID-array `unnest` parameters (#740, accepted), single-array `unnest`
`WITH ORDINALITY` for parallel arrays (#749, accepted), `LATERAL` and derived
relations (#741, accepted), `UNION ALL` (#742, accepted), and `int8multirange` parameters
and containment (#743, accepted).
