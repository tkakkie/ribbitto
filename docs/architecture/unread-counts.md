# Unread counts

*Planned* (M4, [decision 32](../decisions/32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md)):
how read state is stored, written and counted, and what that costs. The
rules are in [unread](../domain/unread.md).

## Storage

An `unread` module owns the read state and depends on `conversation`'s API
([decision 27](../decisions/27-channels-topics-and-messages-are-one-conversation-module.md)'s
growth rule):

| Table | Key | Columns |
|---|---|---|
| `channel_read` | `(organization_id, channel_id, member_id)` | none besides the key: the row that serialises one member's writes in one channel |
| `read_range` | `(organization_id, channel_id, member_id, lo)` | `hi bigint`: the channel's messages with `lo ≤ event_seq < hi` are read |
| `topic_read_floor` | `(organization_id, topic_id, member_id)` | `channel_id`, `floor_seq bigint`: the topic's messages with `event_seq ≤ floor_seq` are read, except those with `moved_event_seq > floor_seq` |

Ranges are disjoint and never touch. The first starts at 0; its `hi` is
the **prefix end** `P`: every message below `P` is read. Without rows, the
read state is `[0, joined_event_seq + 1)`. All keys include
`organization_id`, with composite foreign keys to `member`, `channel` and
`topic`.

`conversation` extends `message` with a nullable `moved_event_seq`, the
sequence of its latest move, written by branching in its transaction, and
a partial index `(organization_id, topic_id, moved_event_seq) WHERE
moved_event_seq IS NOT NULL`. Its existing indexes on `(organization_id,
channel_id, event_seq)` and `(organization_id, topic_id, event_seq)` serve
the rest. `unread` reads messages only through `conversation`'s API, bound
to the caller's snapshot or transaction; the page snapshot reaches `unread`
through an injected factory, as it reaches org's and identity's data
([cross-feature access](cross-feature-access.md)). The queries use only
comparisons, `count` and `max`, which the [query gate](import-checks.md)
accepts.

## Writes

Every write first locks the `channel_read` row (inserting it if missing),
then adds ranges and merges them with their neighbours, so concurrent
writes queue, commute and never remove a range. A newly read message `m`
adds `[p + 1, n)`, with `p` the channel's previous message (or 0) and `n`
its next (or `m + 1`): a gap between ranges therefore always starts at an
unread message.

- **Feed, cursor `S`:** adds `[0, n)`, `n` being the channel's first
  message above `S`, or `S + 1`: one probe, then merging.
- **Topic view, cursor `S`:** reads every unread message of the topic up
  to `S` (the scans below, without their cap), excluding
  `moved_event_seq > S`; adds a range for each, with its neighbours from one
  probe each; then raises the floor to `S` if it is lower. The floor
  commits with the ranges. Its cost is the topic's unread messages, paid
  once.
- **Posting** with the composer's page cursor `S`: the same write as
  reading the page's scope up to `S`, then a range for the new message, so
  a member's own posts are read. From a topic view, the floor also rises to
  the new message when no message of the topic has an `event_seq` or a
  `moved_event_seq` between `S` and it (two probes).

A cursor above the organisation's committed `event_seq` is refused, so any
later move has a higher sequence than the floor it raises.

## Reads

Every count stops at 100 (shown as `99+`). Predicates are exact:
unread means `event_seq ≥ P` and in no range.

- **Channels in the sidebar,** one batch: for each channel, `unread` reads
  its first 101 ranges at or after `P`; the gaps between them, and after
  the last, go to one `conversation` statement that counts each channel's
  messages in its gaps, stopping at 100. Every bounded gap holds an unread
  message, so a channel reads at most 101 range rows and 100 message rows:
  `O(min(u, 100) · log n)` for `u` unread messages; a channel with none
  costs one empty probe of the open gap after the last range.
- **Topics of the current channel** (at most 50), one batch: for each
  topic, its messages with `event_seq ≥ P` and above its floor, in order
  from the topic index, checked against the ranges, until 100 are unread;
  plus its messages with `P ≤ event_seq ≤ floor_seq` and
  `moved_event_seq > floor_seq`, from the partial index, checked the same
  way. Rows read are the unread messages (up to 100) and the read ones
  above the floor: the member's own posts made while an earlier message of
  the topic was unread, and read messages moved in since the floor. Both
  grow only with activity since the member last read the topic, whose next
  read raises the floor past them.
- **First unread** (the divider): for the feed, the first message of the
  first gap; for a topic, the lower of the first unread row from the topic
  index and the lowest unread `event_seq` among the moved-in rows, which are
  read in full (they are ordered by `moved_event_seq`, not `event_seq`).
  Counts' caps never apply here.

## Expected load

Per page load: the two batches and one first-unread lookup, in the page's
snapshot. For a member of 100 channels, each with fewer than 100 unread
messages in one gap, the sidebar reads about 100 range rows and makes 100
index probes, reading up to a few thousand index rows; topic counts add up
to 50 probes and 5,000 rows. Per page view, one POST, plus the visible
page's coalesced POSTs (#710) and sidebar recounts (#286). Posting adds one
lock and one or two range rows for its author.

## Limits

A member who reads only topic views while another topic keeps gaining
messages adds one range per skipped run, about 100 bytes and an index entry
each, until a feed read merges them into one. Counts and writes stay local
(each touches the first 101 ranges, or a range and its neighbours), so the
cost is storage. #283 measures a channel with 10,000 interleaved runs
(write time, rows, count time) before the tables ship.
