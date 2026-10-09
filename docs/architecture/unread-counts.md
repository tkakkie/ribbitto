# Unread counts

*Planned* (M4, [decision 32](../decisions/32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md)):
how read state is stored, written and counted, and what that costs. The
rules are in [unread](../domain/unread.md).

## Storage

`conversation` owns the read state, because counts join `message` and must
come from the page's own snapshot (`conversation.Reader`, decision 27):

| Table | Key | Columns |
|---|---|---|
| `channel_read` | `(organization_id, channel_id, member_id)` | `read_seqs int8multirange` |
| `topic_read_floor` | `(organization_id, topic_id, member_id)` | `channel_id`, `floor_seq bigint` |

`message` gains a nullable `moved_event_seq`, the sequence of its latest
move, written by branching in its transaction, with a partial index
`(organization_id, topic_id, moved_event_seq) WHERE moved_event_seq IS NOT NULL`.
The existing indexes `message (organization_id, channel_id, event_seq)` and
`(organization_id, topic_id, event_seq)` serve every other read. Both new
tables reference `member` and `channel`, or `topic`, with composite foreign
keys that include `organization_id`.

## Writes

Each write updates one `channel_read` row under its row lock
(`INSERT … ON CONFLICT DO UPDATE SET read_seqs = channel_read.read_seqs + EXCLUDED.read_seqs`),
so concurrent writes commute and none removes a range:

- **Feed, cursor `S`:** adds `(, n)`, where `n` is the channel's first
  message above `S`, or `S + 1`: one index probe.
- **Topic view, cursor `S`:** finds the topic's unread messages up to `S`
  as the count below does, excluding `moved_event_seq > S`, together with
  each one's previous and next channel message (`LAG` and `LEAD` over the
  channel's messages in that span), adds one range per message, and raises
  the floor to `S` (`GREATEST`). Its cost is the channel's messages since
  the topic was last read, paid once. A cursor above the organisation's
  committed `event_seq` is refused, so every later move has a higher
  sequence than the floor it raises.
- **Posting:** the author's own message, one range, in the posting
  transaction.

## Reads

Every count stops at 100 (shown as `99+`). Let `F` be the end of the read
prefix: the upper bound of the set's first range.

- **Channels in the sidebar,** one statement for all of them: for each
  channel, the gaps of `read_seqs` above `F` in order (`unnest` of the
  complement), each probed with a range scan of the channel index, stopping
  at 100 rows. Every gap holds an unread message, so a channel costs at most
  `min(gaps, 100)` probes and 100 rows: `O(min(u, 100) · log n)` for `u`
  unread messages and `n` messages.
- **Topics of the current channel** (at most 50), one statement: for each
  topic, its messages above the higher of `F` and its floor, from the topic
  index, that are not in `read_seqs`, stopping at 100; plus its messages at
  or below the floor whose `moved_event_seq` is above it, from the partial
  index, that are not in `read_seqs`. Rows read are the topic's unread
  messages (up to 100) and read messages moved into it, at most 100 per
  move.
- **First unread** (the divider): the first row of the same scans.

Without a row, the set is `(, joined_event_seq + 1)`, read in the same
statements.

## Expected load

Per page load: two count statements and one first-unread lookup, in the
page's snapshot. For a member of 100 channels, each with fewer than 100
unread messages in one gap, the sidebar statement is about 100 index probes
and at most a few thousand index rows; topic counts add up to 50 probes and
5,000 rows. Per page view, one POST, plus the visible page's coalesced POSTs
(#710) and sidebar recounts (#286). Posting adds one row update for its
author.

## Limits

- **Fragmentation.** A member who reads only topic views while another
  topic gains messages adds one gap per skipped run. Counts stay bounded by
  the 100 cap, but `read_seqs` grows by 16 bytes per gap and is rewritten
  by every write; a feed read collapses it to one range. #283 measures a
  channel with 10,000 interleaved runs (write time, row size, count time)
  before the table ships.
- **Floors after moves.** A topic whose floor many moves have bypassed
  reads those moved messages on every count; the next topic-view read
  raises the floor past them.
