# 32. Read state is a set of read ranges per member and channel

**Decided** (#282; supersedes nothing; settles the unread model that
[decision 21](21-topics-inside-channels-a-default-topic-and-branching-instead-of-threads.md)
left to M4). *Planned* for M4; rules in [unread](../domain/unread.md),
storage and cost in [unread counts](../architecture/unread-counts.md).

- **Read ranges.** A member's read state in a channel is a set of
  `message.event_seq` ranges, one row each (`read_range`), starting with
  everything up to `joined_event_seq`. A message is read when its sequence
  is in the set. Writes add and merge ranges under a per-member, per-channel
  lock, so the set only grows. Every gap holds an unread message of the
  channel.
- **Two reading scopes.** The feed reads every message of the channel up to
  the cursor the page has applied; a topic view reads only that topic's
  messages, except any moved in after that cursor. Only POSTs advance read
  state: the first is bounded by the page's snapshot, later ones (#710) by
  what the page has received. Without JavaScript, a *Mark as read* form sends
  the same POST. Posting reads its page's scope too, and a member's own
  posts are read for them.
- **Counts.** A channel's count is all its unread messages, the sum over all
  its topics; a topic's is those in that topic. Both are read in one
  batch per list (the sidebar's channels, the current channel's topics),
  capped at 100 each. Channel counts probe the set's first gaps; topic
  counts start at a per-topic floor (`topic_read_floor`) and look up
  messages moved in since (`message.moved_event_seq`). Nothing reads
  `event_log`.
- **The divider** is the scope's first unread message as the page rendered
  it, carried by the page for the whole visit; nothing stores a previous
  position.
- **Ownership.** Following decision 27's growth rule, an `unread` module
  owns the read-state tables and reads messages only through
  `conversation`'s snapshot-bound API; `conversation` owns
  `message.moved_event_seq`, which extends its messages. The page snapshot
  reaches `unread` through an injected factory.

**Why:** a set of sequences cannot be changed by a move, so both topic tests
hold by construction: branching keeps `event_seq`, and a topic view adds
only its own messages. A moved unread message stays unread whatever the
destination's state, with no write per member when messages move. With
gaps that always hold an unread message, a per-topic floor and a cap of
100, a channel count reads at most 101 ranges and 100 messages, and a topic
count reads its unread messages up to 100 plus the read ones that arrived
above its floor since the member last read that topic: their own posts and
read messages moved in (maintainer's directions, 2026-10-09: read ranges
first).

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

Read ranges cost storage when a member reads only topic views while
another topic keeps growing: the set gains a row per skipped run until a
feed read merges them. Counts and writes stay local, so #283 measures that
case (10,000 runs) before the tables ship. One row per range, rather than
an `int8multirange` column, keeps reads to the first gaps and fits the
query gate's comparisons, `count` and `max`.
