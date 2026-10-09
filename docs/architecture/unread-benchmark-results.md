# Unread benchmark results

Results of `TestUnreadBench` (#283), which measures the statement shapes of
[unread counts](unread-counts.md) under
[decision 32](../decisions/32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md)
before anything is built. How to run it is in
[unread counts](unread-counts.md#running-the-benchmark). The maintainer
decides go or no-go from these results; the decision is recorded below.

## Run, 2026-10-10

- **Machine:** darwin/arm64, 10 CPUs, Go 1.27.2. Not idle: other
  applications kept the load average between 2.9 and 4.6 during the runs.
- **Database:** PostgreSQL 18.6 (`postgres:18` in Docker on the same
  machine, loopback), default settings: `shared_buffers` 128 MB,
  `work_mem` 4 MB, `effective_cache_size` 4 GB, `jit` on,
  `max_parallel_workers_per_gather` 2, `random_page_cost` 4. A dedicated
  container, restarted before each run; no other container ran.
- **Method:** one connection, serial; `ANALYZE` after seeding each
  scenario; per operation 3 warm-ups and 20 measured repetitions,
  alternating the normal and the stressed member on the same data; each
  write in a transaction rolled back afterwards. Two full runs; the tables
  give both (run 1; run 2). Times include the loopback round trips from Go
  and exclude `BEGIN`, `ROLLBACK`, `EXPLAIN` and the diagnostic range count
  logged after each write. Rows visited and shared
  buffers come from one `EXPLAIN (ANALYZE, BUFFERS)` of run 1 (rows
  include filter removals and loops; small tables can get sequential scans,
  so a few normal rows-visited counts are not comparable).
- **Indexes:** `message` (`organization_id, channel_id, event_seq`),
  (`organization_id, topic_id, event_seq`), (`organization_id, event_seq`)
  unique, and the partial (`organization_id, topic_id, moved_event_seq`)
  `WHERE moved_event_seq IS NOT NULL`; the primary keys of `channel_read`,
  `read_range` (`organization_id, channel_id, member_id, lo`) and
  `topic_read_floor`.
- **Data:** one channel with 50 topics. The normal member is caught up: one
  range and every floor at the cursor. The stressed member:
  - *own-posts-N:* one unread message from another member below N of its
    own posts (11 to 10,001 messages), all in one topic above its floor;
  - *moved-10000:* 100 moves of 100 read messages into a topic since its
    floor, plus one unread message (10,001 messages);
  - *fragmented-10000:* 10,000 ranges, alternating read messages in the
    other topics with 9,999 unread ones in one topic (19,998 messages;
    `read_range` 0.9 MB, 1.7 MB with its index).

## Results

Medians and p95 in milliseconds, run 1; run 2. `topic-write` reads the
candidate bounds through `conversation`'s statement and passes them to one
set-based `unread` statement; `topic-write-per-message` merges one message
per statement, for comparison.

#### own-posts-10

| Operation | Normal, median/p95 | Stressed, median/p95 | Rows visited (normal/stressed) | Shared buffers (normal/stressed) |
|---|---|---|---|---|
| step1 | 0.20/0.23; 0.20/0.21 | 0.20/0.22; 0.20/0.23 | 3/3 | 1/1 |
| step2 | 0.21/0.25; 0.22/0.29 | 0.21/0.23; 0.22/0.25 | 11/21 | 1/2 |
| step3-load-decode | 0.26/0.30; 0.26/0.29 | 0.26/0.29; 0.26/0.28 | 106/106 | 4/4 |
| step4-topics | 0.32/0.35; 0.32/0.34 | 0.33/0.36; 0.33/0.35 | 1122/1113 | 102/103 |
| feed-write | 0.54/0.60; 0.56/0.63 | 0.54/0.60; 0.56/0.61 | 19/19 | 11/13 |
| topic-write | 1.01/1.05; 1.02/1.08 | 1.04/1.14; 1.05/1.12 | 137/171 | 18/20 |
| topic-write-per-message | 0.90/0.95; 0.92/0.99 | 1.09/1.14; 1.12/1.20 | 131/169 | 19/31 |

#### own-posts-100

| Operation | Normal, median/p95 | Stressed, median/p95 | Rows visited (normal/stressed) | Shared buffers (normal/stressed) |
|---|---|---|---|---|
| step1 | 0.18/0.21; 0.19/0.20 | 0.19/0.22; 0.19/0.19 | 3/3 | 1/1 |
| step2 | 0.21/0.25; 0.21/0.24 | 0.22/0.29; 0.21/0.24 | 101/201 | 2/4 |
| step3-load-decode | 0.26/0.28; 0.26/0.28 | 0.26/0.31; 0.26/0.28 | 106/106 | 4/4 |
| step4-topics | 0.55/0.59; 0.56/0.59 | 0.65/0.68; 0.65/0.67 | 10201/10203 | 203/204 |
| feed-write | 0.57/0.61; 0.55/0.63 | 0.57/0.60; 0.55/0.58 | 109/109 | 12/14 |
| topic-write | 1.07/1.13; 1.02/1.07 | 1.10/1.16; 1.06/1.11 | 317/420 | 21/24 |
| topic-write-per-message | 0.94/1.02; 0.93/1.00 | 1.15/1.22; 1.13/1.18 | 311/418 | 22/35 |

#### own-posts-1000

| Operation | Normal, median/p95 | Stressed, median/p95 | Rows visited (normal/stressed) | Shared buffers (normal/stressed) |
|---|---|---|---|---|
| step1 | 0.19/0.21; 0.19/0.22 | 0.19/0.20; 0.18/0.35 | 3/3 | 1/1 |
| step2 | 0.21/0.23; 0.21/0.22 | 0.21/0.23; 0.21/0.25 | 0/1 | 2/5 |
| step3-load-decode | 0.26/0.29; 0.25/0.27 | 0.27/0.29; 0.26/0.31 | 106/106 | 4/4 |
| step4-topics | 0.29/0.31; 0.29/0.32 | 3.08/3.22; 3.06/3.26 | 0/50052 | 204/1355 |
| feed-write | 0.57/1.07; 0.56/0.59 | 0.58/0.79; 0.56/0.60 | 8/8 | 12/14 |
| topic-write | 1.04/1.11; 1.04/1.09 | 1.18/1.28; 1.15/1.25 | 114/2119 | 19/69 |
| topic-write-per-message | 0.94/1.02; 0.96/1.06 | 1.26/1.37; 1.29/1.36 | 108/2117 | 20/80 |

#### own-posts-10000

| Operation | Normal, median/p95 | Stressed, median/p95 | Rows visited (normal/stressed) | Shared buffers (normal/stressed) |
|---|---|---|---|---|
| step1 | 0.20/0.22; 0.19/0.21 | 0.20/0.22; 0.19/0.21 | 3/3 | 1/1 |
| step2 | 0.21/0.23; 0.21/0.23 | 0.21/0.25; 0.22/0.25 | 0/1 | 2/5 |
| step3-load-decode | 0.26/0.27; 0.25/0.28 | 0.26/0.27; 0.26/0.29 | 106/106 | 4/4 |
| step4-topics | 0.32/0.40; 0.32/0.44 | 27.33/27.53; 27.44/27.75 | 0/500052 | 204/11755 |
| feed-write | 0.57/0.62; 0.58/0.61 | 0.57/0.60; 0.57/0.62 | 8/8 | 12/14 |
| topic-write | 1.06/1.16; 1.06/1.13 | 2.07/2.16; 2.08/2.14 | 114/20119 | 19/459 |
| topic-write-per-message | 0.97/1.02; 0.97/1.02 | 2.18/2.22; 2.17/2.25 | 108/20117 | 20/470 |

#### moved-10000

| Operation | Normal, median/p95 | Stressed, median/p95 | Rows visited (normal/stressed) | Shared buffers (normal/stressed) |
|---|---|---|---|---|
| step1 | 0.18/0.19; 0.18/0.21 | 0.18/0.21; 0.18/0.21 | 3/3 | 1/1 |
| step2 | 0.22/0.42; 0.20/0.22 | 0.22/0.32; 0.21/0.22 | 0/1 | 2/5 |
| step3-load-decode | 0.20/0.31; 0.20/0.25 | 0.20/0.41; 0.20/0.22 | 106/106 | 4/4 |
| step4-topics | 0.28/0.43; 0.26/0.29 | 1.46/2.01; 1.43/1.52 | 0/20002 | 204/710 |
| feed-write | 0.53/0.72; 0.51/0.56 | 0.52/0.65; 0.51/0.59 | 8/8 | 12/14 |
| topic-write | 0.83/0.93; 0.84/0.90 | 1.97/2.28; 1.83/1.95 | 114/20119 | 19/459 |
| topic-write-per-message | 0.83/1.02; 0.82/0.91 | 2.00/2.24; 2.02/2.16 | 108/20117 | 20/470 |

#### fragmented-10000

| Operation | Normal, median/p95 | Stressed, median/p95 | Rows visited (normal/stressed) | Shared buffers (normal/stressed) |
|---|---|---|---|---|
| step1 | 0.18/0.20; 0.18/0.20 | 0.22/0.42; 0.23/0.42 | 1/101 | 3/4 |
| step2 | 0.22/0.26; 0.22/0.26 | 0.35/0.80; 0.36/0.58 | 0/100 | 2/300 |
| step3-load-decode | 0.29/0.47; 0.27/0.29 | 2.42/3.37; 2.38/3.23 | 102/10102 | 8/119 |
| step4-topics | 0.32/0.42; 0.34/0.38 | 6.08/7.90; 6.35/7.61 | 0/10101 | 306/10379 |
| feed-write | 0.59/0.69; 0.58/0.62 | 3.37/4.77; 3.45/4.10 | 4/10003 | 16/20220 |
| topic-write | 1.23/1.41; 1.20/1.42 | 87.31/89.25; 88.41/93.92 | 106/90098 | 24/90666 |
| topic-write-per-message | 1.19/1.35; 1.20/1.40 | 10546.21/10887.14; 10575.28/10928.73 | 104/80097 | 28/56647289 |

## What the runs show

- **Own posts above a floor:** the 50-topic count (step 4) grew with them:
  medians of 0.33, 0.65, 3.1 and 27.4 ms for 10, 100, 1,000 and 10,000
  posts, against about 0.3 ms caught up; at 10,000 it visited about 500,000
  rows. The topic-view write grew too, from about 1.05 to 2.07 ms
  (set-based) and from 1.1 to 2.2 ms (per message). Steps 1–3 and the feed
  write stayed between 0.2 and 0.6 ms.
- **Moved messages:** with 10,000 read messages moved in since a floor,
  step 4 took about 1.45 ms (0.27 ms caught up) and the set-based
  topic-view write 1.8–2.0 ms (0.8 ms).
- **10,000 ranges:** steps 1 and 2 stayed under 0.4 ms (median); step 3
  took about 2.4 ms (p95 about 3.3 ms), step 4 6.1–6.4 ms (p95 about
  7.8 ms), and the feed read merging every range about 3.4 ms. For the
  9,999 unread messages of the topic, the set-based topic-view write took
  87–88 ms and the per-message write 10.5–10.6 s. In this fixture the
  tested set-based write was therefore about 120 times faster; these runs
  compare only these two shapes, not other batching.
- An earlier version of the harness, whose per-message merges also scanned
  every lower range and whose timings included a diagnostic count, measured
  the per-message write at 8.2 s; these runs replace it.
- Only one channel was counted in steps 1–2, no writer ran concurrently, and
  rolled-back writes can leave dead tuples; these runs say nothing about
  those cases, and no limit beyond the measured sizes follows from them.

## Decision

Pending: the maintainer decides go or no-go from these results (#283).
