# Unread counts

How read state is stored, written and counted, and what that costs
([decision 32](../decisions/32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md)).
The move column, read-state tables, range store and feed write are current;
HTTP callers, topic and posting writes, and counts are *planned* (M4). The
rules are in [unread](../domain/unread.md).

## Storage

The `unread` module owns the current read-state tables and uses
`conversation`'s API
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
tables; tablecheck's ownership rule and its fail-closed checks stay as they
are. The planned page snapshot (`conversation.Reader`) reaches `unread` through an
injected, snapshot-bound factory, as it reaches org's and identity's data
([cross-feature access](cross-feature-access.md)); `unread` in turn calls
`conversation`'s snapshot-bound message queries. Read sets cross the
boundary as values: arrays of `(lo, hi)` bounds, or one `int8multirange`
parameter. Every statement below runs in the page's snapshot, and none runs
once per channel or per topic.

| Step | Owner | Input | Statement | Output |
|---|---|---|---|---|
| 1 | `unread` | member, the sidebar's channel IDs | for each channel, its first 101 ranges by `lo` (`LATERAL … ORDER BY lo LIMIT 101`) | `P` and the first gaps per channel |
| 2 | `conversation` | parallel arrays of channel ID, gap `lo`, gap `hi` | for each channel, its messages in its gaps in order, at most 100 rows per channel (a `LATERAL` per channel over its gaps, `LIMIT 100`), counted and grouped by channel; the feed's first unread is the first row | channel counts, the feed's first unread |
| 3 | `unread` | member, current channel, its listed topic IDs plus the selected topic when the list omits it | its ranges at or above `P`, and the topics' floors | the read set above `P` and a floor for every topic (`P − 1` without a row) |
| 4 | `conversation` | the read set as one `int8multirange`, `P`, per-topic `(id, floor)` arrays | per topic: its messages with `event_seq ≥ P` and above its floor, from the topic index in order, that the read set does not contain, at most 100 (`LATERAL … LIMIT 100`); plus its messages with `P ≤ event_seq ≤ floor` and `moved_event_seq > floor`, from the partial index, not contained; counted per topic; for the selected topic only, its lowest unread `event_seq` | topic counts, a topic view's first unread |

Steps 1–2 serve the channel list and steps 3–4 the topic list: two
statements per list, four per page load, whatever the number of channels or
topics. The cap applies inside steps 2 and 4, per channel and per topic;
the first-unread lookups take no cap. The [query gate](import-checks.md) accepts
`unnest` of `bigint[]` (#727) and `uuid[]` (#740) parameters, parallel arrays
joined `WITH ORDINALITY` (#749); `LATERAL` (#741), `UNION ALL` (#742) and
`int8multirange` (#743) remain planned.

## Cost of the reads

`K` channels in the sidebar, `T` ≤ 51 topics (the listed ones and the selected one), `n` messages in an
index, `R` ranges of the current channel at or above `P`:

- **Step 1:** `O(K · (log n + 101))` range rows.
- **Step 2:** every bounded gap holds an unread message, so each channel
  needs at most 101 gaps and reads at most 100 message rows:
  `O(K · (101 · log n + 100))`. A channel with nothing unread costs one
  empty probe of the open gap after its last range.
- **Step 3:** `O(R + T)` rows; `R` is 1 in normal use and grows with
  fragmentation (below).
- **Step 4:** decoding the parameter is `O(R)`, and each candidate row costs
  `O(log R)` to test. For the count, the rows read per topic are its unread
  messages (up to 100), the read messages moved in since its floor (up to
  100 per move), and the read messages above its floor: the member's own posts made after
  an earlier message of the topic that the page had not yet shown. In
  normal use the stream shows a new message before the member's next post,
  so there are few. While the stream lags or is disconnected, every own
  post in that topic adds one, **without a bound**. #283 benchmarks it;
  if the cost is unacceptable, topic counts switch to probing the read
  set's gaps per topic before implementation starts. The first unread
  takes no cap: the moved branch is ordered by `moved_event_seq`, so finding
  its lowest `event_seq` reads every message moved into the topic since its
  floor, unread or not (1,000 moved messages mean 1,000 rows).

## Writes

The current store locks the `channel_read` row (inserting it if missing),
inserts the join prefix before any other range, and unions one range. A
savepoint handles concurrent creation without aborting the caller's transaction.
It finds the primary-key predecessor, deletes only overlapping or touching
rows between that predecessor and the new upper end, and inserts their union.
All of this commits or rolls back with the caller.

Every reading write first locks `channel_read` (inserting it if missing),
then unions ranges: concurrent writes
queue and commute, and none removes a read message. A newly read message
`m` adds `[p + 1, n)`, with `p` the channel's previous message (or 0) and
`n` its next (or `m + 1`), so every bounded gap contains an unread message.

- **Feed, cursor `S` (current):** `unread.FeedWriter` uses injected factories
  to read org's committed cursor and conversation's next message in the caller's
  transaction, then unions the prefix. One probe finds the first message above
  `S` (`n`, or `S + 1`), then `[0, n)`: it deletes every range it absorbs,
  so its cost is proportional to the ranges merged, `O(R)` after heavy
  fragmentation and `O(1)` in normal use.
- **Topic view, cursor `S` (planned):** set-based, never one statement per message.
  `conversation` returns, in one statement, the bounds `[p + 1, n)` of the
  topic's unread messages up to `S` (step 4's shape without its cap,
  excluding `moved_event_seq > S`); `unread` passes them as arrays (#727) to
  one statement that coalesces them, merges only the ranges they overlap or
  touch (found by the primary key) and raises the floor to `S` if lower, in
  the same transaction. Cost: `O(R)` for the read set and the candidate
  rows; 87–88 ms for 9,999 messages over 10,000 ranges in the benchmark.
- **Posting (planned)** with the composer's cursor `S`: the same write as the page's
  scope up to `S`, then a range for the new message; from a topic view the
  floor also rises to the new message when no message of the topic has an
  `event_seq` or `moved_event_seq` strictly between `S` and it. `S` is the
  newest durable sequence the page has applied and shown, never a sequence
  only received or the post's own response, so posting never reads a
  message the member has not seen.

A cursor above the organisation's committed `event_seq` is refused, so any
later move has a higher sequence than the floor it raises.

## Expected load

Per page load, four statements in the page's snapshot. For a member of 100
channels, each with fewer than 100 unread messages in one gap and an
unfragmented read set, step 2 makes about 100 index probes and reads at
most 9,900 index rows (99 per channel); step 4 adds up to 51 probes and 5,100 rows for
the counts, plus, for the first unread, every message moved into the
selected topic since its floor. Per page view, one POST, plus the visible
page's coalesced POSTs (#710) and sidebar recounts (#286). Posting costs
its scope's read (with any merges) and one range row for its author.

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
`topic-write` measures the set-based flow under Writes; statement count is
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
