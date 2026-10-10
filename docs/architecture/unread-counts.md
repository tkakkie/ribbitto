# Unread counts

How read state is stored and counted (what that costs is in
[unread count costs](unread-count-costs.md), writes are in
[unread writes](unread-writes.md))
([decision 32](../decisions/32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md)).
The move column, read-state tables, range store, feed, topic and branch-notice
reads, the visible-page reading POSTs, posting writes and channel and topic counts
are current; page binding is *planned* (M4). The rules are in
[unread](../domain/unread.md).

## Storage

`unread` owns the read-state tables and uses `conversation`'s API
([decision 27](../decisions/27-channels-topics-and-messages-are-one-conversation-module.md)'s
growth rule):

| Table | Key | Columns |
|---|---|---|
| `channel_read` | `(organization_id, channel_id, member_id)` | none besides the key: the row that serialises one member's writes in one channel |
| `read_range` | `(organization_id, channel_id, member_id, lo)` | `hi bigint`: the channel's messages with `lo ≤ event_seq < hi` are read |
| `topic_read_floor` | `(organization_id, topic_id, member_id)` | `channel_id`, `floor_seq bigint`: the topic's messages with `event_seq ≤ floor_seq` are read, except those with `moved_event_seq > floor_seq` |

Ranges are disjoint and never touch. The first starts at 0; its `hi` is
the **prefix end** `P`: every message below `P` is read. Without rows, the
read state is `[0, joined_event_seq + 1)`: step 1 and step 3 supply that
range for a channel with no rows, and the first write inserts it before its
own ranges, so the first range always starts at 0. A topic without a
`topic_read_floor` row has no floor: its scan starts at `P` and it has no
moved branch, which step 3 expresses as the floor `P − 1`. All keys include
`organization_id`, with composite foreign keys to `member`, `channel` and
`topic`. `conversation` already stores nullable `message.moved_event_seq`,
the sequence of its latest move, which branching writes with `topic_id` in
its transaction. Its partial index is `(organization_id, topic_id,
moved_event_seq) WHERE moved_event_seq IS NOT NULL`.

## The API between the modules

`unread` never reads `message`, and `conversation` never reads `unread`'s
tables. The planned page snapshot (`conversation.Reader`) reaches `unread` through an
injected, snapshot-bound factory, as it reaches org's and identity's data
([cross-feature access](cross-feature-access.md)); `unread` in turn calls
`conversation`'s snapshot-bound message queries. Read sets cross as arrays of `(lo, hi)` bounds, or one `int8multirange`
parameter. These statements share the page snapshot, with none per channel or topic.

| Step | Owner | Input | Statement | Output |
|---|---|---|---|---|
| 1 (current) | `unread` | member, the sidebar's channel IDs | for each channel, its first 101 ranges by `lo` (`LATERAL … ORDER BY lo LIMIT 101`) | `P` and the first gaps per channel |
| 2 (current) | `conversation` | parallel arrays of channel ID, gap `lo`, gap `hi` | for each channel, its messages in its gaps in order, at most 100 rows per channel (a `LATERAL` per channel over its gaps, `LIMIT 100`), counted per channel; `ORDER BY … LIMIT 1` returns its first unread | channel counts, the feed's first unread |
| 3 (current) | `unread` | member, current channel, its listed topic IDs plus the selected topic when the list omits it | its ranges at or above `P`, and the topics' floors | the read set above `P` and a floor for every topic (`P − 1` without a row) |
| 4 (current) | `conversation` | the read set including `[0, P)` as one `int8multirange`, `P`, per-topic `(id, floor)` arrays | per topic: messages above `P − 1` and its floor, plus messages at or below its floor with `moved_event_seq > floor`; both exclude read-set containment and share an ordered cap of 100; only the selected topic returns its lowest unread `event_seq`, uncapped | topic counts, a topic view's first unread |

Step 1 uses `unreadpg.ChannelRangesIn` in the caller's snapshot. Go derives
`P` and at most 100 gaps; range 101 only bounds gap 100. Channels without
rows retain the supplied join prefix and one open gap. `unread.ChannelCounts`
deduplicates sidebar IDs and builds gap IDs and bounds together. Step 2 uses
`conversationpg.ChannelUnreadIn`: ordinal joins pair length-checked arrays;
Go supplies inclusive lower bounds as their predecessors. The channel cap orders
by gap ordinal and message sequence, returning at most 100 rows across all topics,
including an unread branch notice. Incremental sorting may finish a gap and read
ahead before stopping. Both statements share the caller's snapshot.

Step 3 uses `unreadpg.TopicStateIn` in the caller's snapshot. `ReadTopicState`
combines channel ranges and requested floors with `UNION ALL`, returning each
range once. Go separates the prefix, retains the join prefix without rows and
supplies `P − 1` for missing floors. Listed and selected topic IDs are
deduplicated before the statement, with at most 51 distinct IDs. The stored
prefix starts at zero; every other disjoint, non-touching range lies above `P`.

Step 4 uses `conversationpg.TopicUnreadIn`. Ordinal joins pair IDs and floors;
Go checks equal lengths, computes `P − 1` and adds `[0, P)` to the multirange.
All branches exclude read-set containment; only above-floor branches have a
lower sequence bound. Ordered count branches each cap at 100, then share an
ordered cap of 100.
Only the selected topic probes above the floor and materialises its unread
moved candidates before choosing the lowest `event_seq`, without a candidate
cap. `unread.TopicCounts`, wired by `newTopicCounts`,
combines steps 3–4 in the caller's snapshot; page binding remains planned.

Steps 1–2 serve the channel list and steps 3–4 the topic list: two
statements per list, four per page load, whatever the number of channels or
topics. The cap applies inside steps 2 and 4, per channel and per topic;
the first-unread lookups take no cap. The [query gate](query-gate.md) accepts
`unnest` of `bigint[]` (#727) and `uuid[]` (#740) parameters, parallel arrays
joined `WITH ORDINALITY` (#749), `int8multirange` `@>` (#743) and `LATERAL`
(#741) and `UNION ALL` (#742).

## Costs

The cost of each step, the expected load, fragmentation, the benchmark and
its results are in [unread count costs](unread-count-costs.md).

## Writes

Feed, topic-view and branch-notice writes are current; their range unions are in
[unread writes](unread-writes.md).

