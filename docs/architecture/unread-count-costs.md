# Unread count costs

Costs of the [counting contract](unread-counts.md)
([decision 32](../decisions/32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md)).

## Cost of the reads

The 100-message caps bound returned unread counts, not all database work.
Channel counting supplies at most 100 gaps per channel, bounded by the first
101 ranges; incremental sorting may read ahead beyond the returned rows.
Parameter pairing and per-channel scans of the bounded count results also
add in-memory work, without revisiting messages.

Topic counts filter read moves (at most 100 messages per move) and own posts
above the floor until a later topic read raises it. This filtering is
**unbounded**. #283 benchmarks stream lag; unacceptable costs require
per-topic gap probes.

On normal pages, only the selected topic gets the uncapped first-unread
lookup. Enumerating unread moved candidates sacrifices early exit for dense
moves. For `M` unread moved candidates, there is **no physical `O(M)` guarantee**:
PostgreSQL can still scan all topic history. #747 re-checks page load.

Counts-only sidebar recounts skip first-unread work, including moved-candidate
materialisation, without reducing the four count statements.
`TestSidebarSkipsFirstUnread` checks skipped work and equal counts, not a
throughput or end-to-end latency guarantee. Snapshot statement budgets and
conditional `SET LOCAL` are described in
[server-side timeouts](../recount-capacity.md#server-side-timeouts).
Total page/recount query counts belong to `TestPageCountsSnapshot` and
`TestSidebarSnapshot`.

## Expected load

For 100 channels, each with fewer than 100 unread messages in one gap and an
unfragmented read set, channel counting makes about 100 index probes and
reads at most 9,900 index rows (99 per channel). Topic counting returns at
most 5,100 candidates across 51 topics (up to 100 per branch before the
shared cap), plus uncapped read filtering and the selected topic's
first-unread work above. These conditions are illustrative load accounting,
not a ceiling on filtering, planner work or latency.

## Fragmentation

A member who reads only topic views while another topic keeps gaining
messages adds one range per skipped run. Channel counting reads only the
first 101 ranges; topic counting and a topic-view write handle all `R`
ranges. A feed read merges them back into one, at `O(R)`.

### Running the benchmark

Benchmark labels: step 1 loads channel ranges; step 2 counts channel messages;
step 3 loads and decodes topic read state; step 4 counts topic messages.

Export `RIBBITTO_TEST_DATABASE_URL` for disposable loopback PostgreSQL
([database tests](../database-tests.md)); run outside CI:

```sh
RIBBITTO_UNREAD_BENCH=1 go test -count=1 -run '^TestUnreadBench$' -v -timeout 30m ./internal/conversation/conversationpg
```

[`TestUnreadBench`](../../internal/conversation/conversationpg/unread_bench_test.go):
`RIBBITTO_UNREAD_BENCH_*` options, defaults, minimums and timing
conditions. First-unread plans and dense-move trade-offs can be reproduced
with the command in
[`TestFirstUnreadBench`](../../cmd/ribbitto/first_unread_bench_test.go).
Recount plans use `RIBBITTO_RECOUNT_MEASURE=1` with
[`TestSidebarSkipsFirstUnread`](../../cmd/ribbitto/recount_first_unread_test.go).
History: [frozen recount record](../measurements/sidebar-counts-history.md).
Deleted EXPLAIN figures and dense-move timings: this page at
`aaad4bf7aea15b7d678681186329e129de54dc34`.

## Results

The **go decision**, its reasons, limits and when to revisit are in
[benchmark results](../measurements/unread-benchmark-results.md#decision).
Read ranges with the topic-floor scan and set-based topic writes were
accepted; this is not a performance guarantee.
