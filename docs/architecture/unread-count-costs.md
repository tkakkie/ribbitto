# Unread count costs

What the [unread counts](unread-counts.md) cost, the load they are expected to
carry, how the read set fragments, and the benchmark behind them
([decision 32](../decisions/32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md)).

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
- **Step 4:** decoding costs `O(R)`; containment costs `O(log R)` per row.
  Counts cap unread candidates at 100 per branch, then 100 together; read
  moves (at most 100 messages per move) and own posts above the floor add
  **unbounded** filtering. #283 benchmarks stream lag; unacceptable costs
  require per-topic gap probes.
  First unread is uncapped: a CTE filters `M` rows moved after the selected
  topic's floor, outputting at most `M`; choosing the lowest sequence costs
  `O(M)`, plus `O(M log R)` containment.
  Materialisation blocks the sequence-order limit at the CTE, removing #779's
  measured history walk, with **no physical `O(M)` guarantee**: PostgreSQL can
  still scan all history.
  PostgreSQL 18.6, default planner settings, ANALYZE, second (warm) EXPLAIN:
  with 1,000 moves and 10k/100k/1M history rows, the old lookup read
  1k/10k/100k rows; the CTE read 1,000 (1 returned, 999 filtered), 27 shared
  hits, zero reads throughout. No moves read zero rows; all-read moves read
  1,000. Dense 100k unread moves cost 26.188 ms for the whole statement
  versus 0.070 ms before: enumerating moves sacrifices early exit.
  Above-floor filtering remains unbounded; #747 re-checks page load. Reproduce
  with
  `RIBBITTO_FIRST_UNREAD_BENCH=1 go test -count=1 -run '^TestFirstUnreadBench$' -v ./cmd/ribbitto`.

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
