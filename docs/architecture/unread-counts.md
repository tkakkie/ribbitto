# Unread counts

How read state is stored and counted, and what that costs (writes are in
[unread writes](unread-writes.md))
([decision 32](../decisions/32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md)).
The move column, read-state tables, range store, feed, topic and branch-notice
reads, the initial reading POSTs, posting writes and channel and topic counts
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
Both count and first-unread branches use the same containment test; only the
above-floor branch has the lower sequence bound. Ordered count branches each
return at most 100 candidates, then share one ordered cap of 100.
Only the selected topic probes each branch by `event_seq` for its lowest
unread, without a candidate cap. `unread.TopicCounts`, wired by `newTopicCounts`,
combines steps 3–4 in the caller's snapshot; page binding remains planned.

Steps 1–2 serve the channel list and steps 3–4 the topic list: two
statements per list, four per page load, whatever the number of channels or
topics. The cap applies inside steps 2 and 4, per channel and per topic;
the first-unread lookups take no cap. The [query gate](query-gate.md) accepts
`unnest` of `bigint[]` (#727) and `uuid[]` (#740) parameters, parallel arrays
joined `WITH ORDINALITY` (#749), `int8multirange` `@>` (#743) and `LATERAL`
(#741) and `UNION ALL` (#742).

## Cost of the reads

`K` channels in the sidebar, `T` ≤ 51 topics (the listed ones and the selected one), `n` messages in an
index, `R` ranges of the current channel at or above `P`:

- **Step 1:** `O(K · (log n + 101))` range rows.
- **Step 2:** every bounded gap holds an unread message, so each channel
  needs at most 100 supplied gaps. Each probe caps message rows at 100;
  a full sort could consume every probe: `O(K · 100 · (log n + 100))`.
  Incremental sorting stops earlier but can read beyond the 100 returned rows.
  A channel with nothing unread costs one empty probe of its open gap.
  These bounds describe stored-row access. Parameter pairing and scanning
  the bounded CTE for each channel's count and first sequence add
  `O(100 · K²)` in-memory work; aggregates never revisit `message`.
- **Step 3:** `O(R + T)` rows; `R` is 1 in normal use and grows with
  fragmentation (below).
- **Step 4:** decoding the parameter is `O(R)`, and each candidate row costs
  `O(log R)` to test. Count work includes unread messages (up to 100 per branch
  before their shared cap), read messages moved in since the floor (at most 100 per move;
  `LIMIT 100` bounds only unread ones), and read posts above the floor. Normally the stream shows a new
  message before the member's next post, so there are few. While the stream
  lags or is disconnected, own posts add work **without a bound**. #283 benchmarks
  it; if unacceptable, counts switch to probing read-set gaps per topic.
  The selected topic's first unread takes no cap. In #746's PostgreSQL 18.6
  EXPLAIN, with default planner settings,
  51 topics, 100,000 target history rows and 1,000 moved-in rows, the fixed moved
  count read only 1,000 moved rows (1 returned, 999 filtered; 32 shared hits for
  the target). The selected moved first-unread lookup walked the topic index in
  `event_seq` order: 1 returned and 9,999 history rows filtered, 247 shared hits.
  Both measurements held with 5,000 read posts above the floor. These are measured
  rows/buffers under those conditions, not guarantees of a particular index.
  Above-floor read filtering and first-unread work remain uncapped; #779 tracks
  bounding the latter, and #747 re-checks it under real page load.

## Writes

Feed, topic-view and branch-notice writes are current; their range unions are in
[unread writes](unread-writes.md).

## Expected load

Four statements run per page snapshot. For 100 channels, each with fewer
than 100 unread messages in one gap and an unfragmented read set, step 2
makes about 100 index probes and reads at most 9,900 index rows (99 per
channel); step 4 returns at most 5,100 candidates across 51 topics
(with up to 100 per branch before the shared cap), plus uncapped read filtering
and the selected topic's first-unread work described above.
Each visit sends one POST, then visible-page coalesced POSTs (#710) and
sidebar recounts (#286). Posting costs its scope's read (with merges)
and one range row for its author.

## Fragmentation

A member who reads only topic views while another topic keeps gaining
messages adds one range per skipped run (about 100 bytes and an index entry
each). Steps 1 and 2 read only the first 101; steps 3 and 4 and a topic-view
write handle all `R`; a feed read merges them back into one, at `O(R)`.
#283 measured 10,000 ranges before the tables shipped.

### Running the benchmark

Export `RIBBITTO_TEST_DATABASE_URL` for disposable loopback PostgreSQL ([database tests](../database-tests.md)); run outside CI:

```sh
RIBBITTO_UNREAD_BENCH=1 go test -count=1 -run '^TestUnreadBench$' -v -timeout 30m ./internal/conversation/conversationpg
```

Optional `RIBBITTO_UNREAD_BENCH_*` suffixes (defaults): `POSTS` (`10,100,1000,10000`), `MOVES` (100), `MOVE_SIZE` (100),
`RANGES` (10000), `TOPICS` (50), `WARMUP` (3), `REPEAT` (20). Use positive integers; `RANGES` and `TOPICS` need at least 2.
Logs include version, settings, volumes, indexes, `ANALYZE`, plans, row visits, buffers, median and p95.
Normal/stressed runs alternate; writes roll back. Timings exclude transaction boundaries and plan instrumentation.
`topic-write` measures the set-based flow in [unread writes](unread-writes.md); statement count is
independent of messages read. Range-count diagnostics run after EXPLAIN totals,
outside timings and instrumentation. `topic-write-per-message` retains one merge
per message for comparison. Both bound lookups by the primary-key predecessor
and the new upper end.

## Results

The two PostgreSQL 18.6 runs on 2026-10-10, their medians and measured
limits are in [benchmark results](unread-benchmark-results.md).

**Decision: go** (maintainer, 2026-10-10): read ranges with the topic-floor
scan; topic-view writes are set-based. Not a performance guarantee; the
reasons, limits and when to revisit are in
[benchmark results](unread-benchmark-results.md#decision).
