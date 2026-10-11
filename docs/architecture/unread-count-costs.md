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
  the bounded CTE for each channel's count and optional first sequence add
  `O(100 · K²)` in-memory work; aggregates never revisit `message`.
- **Step 3:** `O(R + T)` rows; `R` is 1 in normal use and grows with
  fragmentation (below).
- **Step 4:** decoding costs `O(R)`; containment costs `O(log R)` per row.
  Counts cap unread candidates at 100 per branch, then 100 together; read
  moves (at most 100 messages per move) and own posts above the floor add
  **unbounded** filtering. #283 benchmarks stream lag; unacceptable costs
  require per-topic gap probes.
  On normal pages, first unread is uncapped: a CTE filters `M` rows moved after the selected
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

## Counts-only sidebar recounts (#854, unit 2)

Production recounts disable first unread independently of selection. Four count
statements still run: feed recounts use 7 total, listed and unlisted topic
recounts 8, in one begin/commit pair. Normal pages remain 12 feed, 13 topic,
10 members. No execution saving is claimed.

Measured on local PostgreSQL 18.6, default planner settings, ANALYZE and second
(warm) EXPLAIN on 2026-10-12: 10k history messages, 1k unread moves below floor
20,000 and one message above it; join prefix 2. Listed uses one topic, unlisted
uses 50 links plus the selected topic. Both selected counts remain capped at 100.

| Selection | Topic shared hits before → after | Topic statement ms before → after |
|---|---:|---:|
| Listed | 37 → 7 | 0.291 → 0.047 |
| Unlisted | 11,337 → 11,307 | 29.801 → 29.485 |

Before, `CTE moved → Nested Loop` returned 1,000 rows, loops=1, with 27 shared
hits; after, that node and its message scans say `never executed` for both
selections. The selected above-floor first probe also stops; capped count
probes remain. Channel first-sequence scan/sort disappears; message access
and its 4 shared hits remain. All plans had zero shared reads. The unlisted
fixture's empty-topic count filtering still dominates: these single warm plans
show skipped work, not a throughput or end-to-end latency guarantee.

Reproduce with the database and worktree caches exported as in the task:

```sh
RIBBITTO_RECOUNT_MEASURE=1 go test -count=1 -run '^TestSidebarSkipsFirstUnread$' -v ./cmd/ribbitto
```

The test traces the actual recount's two message-count statements and parameters,
replays each with `EXPLAIN (ANALYZE, BUFFERS)`, guards zero first-unread output and
non-execution of moved materialisation, and compares counts with a normal page.
The before run used the same fixture/test with production files restored from
`codex/sidebar-assembly`; its zero-first assertions failed as expected. Normal
page first unread remained 2/2 in both runs. Commands and plan excerpts are in
the local PR handoff.

## Expected load

Four statements run per page snapshot. For 100 channels, each with fewer
than 100 unread messages in one gap and an unfragmented read set, step 2
makes about 100 index probes and reads at most 9,900 index rows (99 per
channel); step 4 returns at most 5,100 candidates across 51 topics
(with up to 100 per branch before the shared cap), plus uncapped read filtering
and the selected topic's first-unread work described above.
Each visit sends one POST, then visible-page coalesced POSTs (#710) and
sidebar recounts (#286). Posting costs its scope's read (with merges)
and one range row for its author. A topic post with a cursor prepares read
state once and reuses it for the own-message union and final floor update.
`TestPostingRead` measures 29 → 23 SQL statements (31 → 25 including BEGIN
and COMMIT) with an existing channel lock row, for clear, intervening-post
and moved-message cases. Each now executes one `LockChannelRead`, one
`TopicReadRanges` and one `TopicReadFloor`; creating a missing lock row adds
its savepoint, insert and retry. This removes executions; no cheaper SQL plan
or posting latency is claimed. Stored ranges and floors remain identical.

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
