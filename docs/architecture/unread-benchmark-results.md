# Unread benchmark results

Results of `TestUnreadBench` (#283), which measures the statement shapes of
[unread counts](unread-counts.md) under
[decision 32](../decisions/32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md)
before anything is built. How to run it is in
[unread counts](unread-counts.md#running-the-benchmark). The maintainer
decides go or no-go from these results; the decision is recorded below.

## Run, 2026-10-10

- **Machine:** darwin/arm64, 10 CPUs, Go 1.27.2. Not idle: other
  applications kept the load average between 3.1 and 4.7 during the runs.
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
  and exclude `BEGIN`, `ROLLBACK` and `EXPLAIN`. Rows visited and shared
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

Medians and p95 in milliseconds, run 1; run 2.

#### own-posts-10
| op | normal med/p95 (r1; r2) ms | stressed med/p95 (r1; r2) ms | rows visited n/s | buffers n/s |
| step1 | 0.21/0.25; 0.20/0.21 | 0.21/0.22; 0.20/0.21 | 3/3 | 1/1 |
| step2 | 0.22/0.25; 0.22/0.26 | 0.23/0.30; 0.23/0.28 | 11/21 | 1/2 |
| step3-load-decode | 0.26/0.29; 0.27/0.34 | 0.27/0.31; 0.27/0.31 | 106/106 | 4/4 |
| step4-topics | 0.32/0.35; 0.33/0.37 | 0.34/0.38; 0.34/0.35 | 1122/1113 | 102/103 |
| feed-write | 0.76/0.97; 0.74/0.80 | 0.78/0.92; 0.73/0.79 | 22/21 | 12/14 |
| topic-write | 1.09/1.20; 1.07/1.18 | 1.13/1.28; 1.08/1.18 | 140/173 | 16/16 |
| topic-write-per-message | 1.13/1.37; 1.15/1.24 | 1.38/1.60; 1.37/1.61 | 134/171 | 20/32 |
#### own-posts-100
| op | normal med/p95 (r1; r2) ms | stressed med/p95 (r1; r2) ms | rows visited n/s | buffers n/s |
| step1 | 0.19/0.23; 0.19/0.23 | 0.20/0.23; 0.19/0.21 | 3/3 | 1/1 |
| step2 | 0.21/0.25; 0.22/0.24 | 0.23/0.27; 0.22/0.24 | 101/201 | 2/4 |
| step3-load-decode | 0.27/0.31; 0.27/0.28 | 0.27/0.30; 0.26/0.31 | 106/106 | 4/4 |
| step4-topics | 0.57/0.66; 0.56/1.33 | 0.66/0.69; 0.66/0.91 | 10201/10203 | 203/204 |
| feed-write | 0.73/0.80; 0.78/1.24 | 0.74/0.87; 0.78/1.06 | 112/111 | 13/15 |
| topic-write | 1.07/1.18; 1.21/1.45 | 1.12/1.26; 1.29/1.84 | 320/422 | 16/16 |
| topic-write-per-message | 1.13/1.21; 1.17/1.59 | 1.32/1.39; 1.42/1.92 | 314/420 | 23/36 |
#### own-posts-1000
| op | normal med/p95 (r1; r2) ms | stressed med/p95 (r1; r2) ms | rows visited n/s | buffers n/s |
| step1 | 0.18/0.21; 0.19/0.27 | 0.18/0.20; 0.19/0.22 | 3/3 | 1/1 |
| step2 | 0.22/0.26; 0.22/0.30 | 0.21/0.30; 0.22/0.25 | 0/1 | 2/5 |
| step3-load-decode | 0.25/0.27; 0.27/0.30 | 0.26/0.29; 0.27/0.30 | 106/106 | 4/4 |
| step4-topics | 0.29/0.30; 0.29/0.32 | 3.03/3.26; 3.02/3.23 | 0/50052 | 204/1355 |
| feed-write | 0.74/0.80; 0.78/0.87 | 0.72/0.79; 0.81/0.89 | 11/10 | 13/15 |
| topic-write | 1.06/1.15; 1.08/1.18 | 1.21/1.30; 1.25/1.36 | 117/2121 | 16/16 |
| topic-write-per-message | 1.13/1.20; 1.15/1.22 | 1.41/1.47; 1.47/1.52 | 111/2119 | 21/81 |
#### own-posts-10000
| op | normal med/p95 (r1; r2) ms | stressed med/p95 (r1; r2) ms | rows visited n/s | buffers n/s |
| step1 | 0.19/0.22; 0.19/0.21 | 0.20/0.27; 0.18/0.20 | 3/3 | 1/1 |
| step2 | 0.22/0.31; 0.21/0.24 | 0.23/0.27; 0.21/0.26 | 0/1 | 2/5 |
| step3-load-decode | 0.27/0.35; 0.26/0.27 | 0.27/0.34; 0.25/0.28 | 106/106 | 4/4 |
| step4-topics | 0.34/0.38; 0.33/0.46 | 27.45/28.01; 27.31/27.80 | 0/500052 | 204/11755 |
| feed-write | 0.76/0.84; 0.73/0.84 | 0.75/0.83; 0.76/0.79 | 11/10 | 13/15 |
| topic-write | 1.08/1.18; 1.09/1.17 | 2.10/2.25; 2.09/2.24 | 117/20121 | 16/16 |
| topic-write-per-message | 1.10/1.21; 1.16/1.23 | 2.36/2.46; 2.37/2.42 | 111/20119 | 21/471 |
#### moved-10000
| op | normal med/p95 (r1; r2) ms | stressed med/p95 (r1; r2) ms | rows visited n/s | buffers n/s |
| step1 | 0.18/0.20; 0.18/0.19 | 0.18/0.22; 0.18/0.19 | 3/3 | 1/1 |
| step2 | 0.20/0.26; 0.22/0.25 | 0.21/0.27; 0.23/0.28 | 0/1 | 2/5 |
| step3-load-decode | 0.20/0.20; 0.22/0.24 | 0.20/0.27; 0.22/0.25 | 106/106 | 4/4 |
| step4-topics | 0.27/0.31; 0.29/0.32 | 1.45/1.78; 1.46/1.61 | 0/20002 | 204/710 |
| feed-write | 0.66/0.74; 0.68/0.81 | 0.67/0.75; 0.67/0.74 | 11/10 | 13/15 |
| topic-write | 0.95/1.07; 1.19/2.28 | 1.92/2.13; 2.17/3.40 | 117/20121 | 16/16 |
| topic-write-per-message | 0.99/1.13; 0.99/1.29 | 2.13/2.30; 2.20/2.43 | 111/20119 | 21/471 |
#### fragmented-10000
| op | normal med/p95 (r1; r2) ms | stressed med/p95 (r1; r2) ms | rows visited n/s | buffers n/s |
| step1 | 0.18/0.21; 0.18/0.21 | 0.22/0.54; 0.23/0.38 | 1/101 | 3/4 |
| step2 | 0.24/0.37; 0.22/0.27 | 0.39/0.89; 0.36/0.64 | 0/100 | 2/300 |
| step3-load-decode | 0.29/0.32; 0.29/0.40 | 2.66/4.13; 2.57/3.98 | 102/10102 | 8/119 |
| step4-topics | 0.34/0.49; 0.37/0.77 | 6.47/8.21; 6.74/8.50 | 0/10101 | 306/10379 |
| feed-write | 0.78/0.90; 0.77/0.84 | 3.77/5.31; 3.75/4.41 | 5/10005 | 20/20334 |
| topic-write | 1.28/1.39; 1.28/1.42 | 84.60/90.20; 83.67/85.76 | 107/90100 | 22/244 |
| topic-write-per-message | 1.41/2.00; 1.35/1.50 | 10538.66/10912.11; 10638.80/11302.67 | 105/80099 | 33/56691659 |

## What the runs show

- **Own posts above a floor:** the 50-topic count (step 4) grew with them:
  medians of 0.34, 0.66, 3.0 and 27.4 ms for 10, 100, 1,000 and 10,000
  posts, against about 0.3 ms caught up; at 10,000 it visited about 500,000
  rows. The other steps did not change.
- **Moved messages:** with 10,000 read messages moved in since a floor,
  step 4 took 1.45 ms (0.28 ms caught up) and the topic-view write about
  2 ms (1 ms).
- **10,000 ranges:** steps 1 and 2 stayed under 0.4 ms (median); step 3
  took 2.6 ms (p95 about 4 ms), step 4 6.5–6.7 ms (p95 about 8.4 ms), and
  the feed read merging every range 3.8 ms. The set-based topic-view write
  of 9,999 unread messages took 84 ms. Writing them one statement per
  message took 10.5–10.6 s; an earlier run, whose merges also scanned every
  lower range, took 8.2 s. A topic-view write must therefore be set-based.
- Only one channel was counted in steps 1–2, no writer ran concurrently, and
  rolled-back writes can leave dead tuples; these runs say nothing about
  those cases, and no limit beyond the measured sizes follows from them.

## Decision

Pending: the maintainer decides go or no-go from these results (#283).
