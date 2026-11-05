# Message paging plans

The 2026-10-18 measurement for #1300 at `406e1121b6cabb64c171c6cd83444c8d66a8deb8`
tested whether [ListMessagesBefore and ListMessagesAfter](../db/queries/conversation/message.sql)
keep useful index conditions under generic plans. **The measured workload needs
no change.** Keep the queries and pool configuration.

## Conditions and scope

PostgreSQL **18.6**, Debian 18.6-1.pgdg13+2, Linux aarch64 (GCC 14.2.0), on a
disposable database in the Codex sandbox. Settings: shared_buffers 128 MiB,
work_mem 4 MiB, effective_cache_size 4 GiB, random_page_cost 4, jit on,
max_parallel_workers_per_gather 2; only plan_cache_mode changed. Goose applied
the real migrations through **00019**.

The queried organisation had one busy channel of 1,000,000 messages, including
200 sparse-topic messages, one every 5,000. The original run also held a second,
unqueried organisation of 100,000 messages, which can affect planner statistics.
Bodies were about 150 characters; quote/pin fields were NULL. Sequences 1–2
preceded messages; the boundary modelled events past retention without backfill
or disabled triggers. One member; no concurrent writes.

A temporary Go driver used pgx v5.11.0's simple protocol and goose v3.29.0,
with one connection per phase. The published psql procedure **was not run as
psql** (psql was unavailable). The database, driver, SQL and raw outputs were
removed after the run. The full published procedure remains retrievable at the
branch's starting main SHA:
`git show 5da6ae9380fae3f749201414b7b94d8844ca5c3d:docs/message-paging-plans.md`.
A rerun needs the two queries in `db/queries/conversation/message.sql`, the data
shape above, forced generic versus forced custom versus auto plans, and
`pg_prepared_statements` counters.

Both query definitions are unchanged between the measured SHA and main
`5da6ae9380fae3f749201414b7b94d8844ca5c3d`; the three indexes below still exist
with the same keys there. This comparison is information, not a guarantee:
identical query text does **not** carry plans or performance forward. The figures
hold for PostgreSQL 18.6, the schema after migration 00019, those indexes, that
data distribution and those settings. Another version, schema, index set, row
count, channel count or distribution is untested.

## Results

All plans were Limit → Index Scan, without sorting, one scan loop. Indexes:

| Code | Index | Keys |
|---|---|---|
| C | message_channel_event_seq_idx | organization_id, channel_id, event_seq |
| T | message_topic_event_seq_idx | organization_id, topic_id, event_seq |
| U | message_organization_id_event_seq_key | organization_id, event_seq (unique) |

O, Cn, Tn and S mean organisation, channel, topic and sequence predicates.
Generic shapes matched EXPLAIN GENERIC_PLAN and forced EXECUTE; custom ones
came from EXECUTE.

| Statement / parameters | Generic index conditions; filter | Custom index conditions; filter |
|---|---|---|
| Before / feed first | C: O, Cn; nullable topic and cursor guards | U: O; Cn |
| Before / feed before | C: O, Cn; nullable topic and cursor guards | U: O, S < cursor; Cn |
| Before / topic first | C: O, Cn; nullable topic and cursor guards | T: O, Tn; Cn |
| Before / topic before | C: O, Cn; nullable topic and cursor guards | T: O, Tn, S < cursor; Cn |
| After / feed | C: O, Cn, S > cursor; nullable topic guard | U: O, S > cursor; Cn |
| After / topic | C: O, Cn, S > cursor; nullable topic guard | T: O, Tn, S > cursor; Cn |

Custom feed plans chose U rather than C because the organisation has one
channel; both indexes remained available. Organisations with many channels were
not measured, and walking U with a channel filter could cost more there. Generic before cannot seek the cursor or topic; after retains
its unguarded sequence condition but cannot seek the topic.

Times below are median EXPLAIN **Execution Time** in ms over five retained runs
(planning excluded), after discarding repetition 0, alternating forced generic
and custom. The working set exceeded shared_buffers; no cache flushes were used.
Every before run returned 51 rows (including the older-page sentinel), every
after run 25 (around-message's newer rows); before cursors were 500003, after
500002, and first-page before cursors were NULL.

| Form | Forced generic ms | Forced custom ms |
|---|---:|---:|
| Feed first | 0.031 | 0.020 |
| Feed before | 62.545 | 0.026 |
| Topic first | 29.089 | 0.058 |
| Topic before | 74.125 | 0.062 |
| Feed after | 0.019 | 0.018 |
| Topic after | 8.630 | 0.026 |

Absolute times depend on cache state and server; the missing index conditions
explain the extra work.

Under **auto on PostgreSQL 18.6**, all six isolated cases had these counters
(generic/custom): execution 5 = **0/5**, 6 = **0/6**, 15 = **0/15**. Both mixed
statements had 0/5, 0/6 and **0/20**. Plans on execution 6 and later retained
the custom shapes above. Five custom executions therefore did not imply a
switch. Generic estimated total Limit costs were 15,647.85 (before) and
14,524.63 (after), versus 10.08–199.27 for custom plans with these parameters;
the unknown limit also affects generic estimates. Auto compares generic cost
with average custom cost, so a small channel could reverse this margin. Median forced custom planning time was
0.034–0.114 ms across cases.

**The forced-plan hazard was confirmed** (forced generic plans lose useful
index conditions). **The suspected regression under the default `auto` mode was
not reproduced** (counters stayed 0/N and custom shapes were kept); "not
reproduced" does not mean "cannot happen". Splitting feed/topic and first/before queries would remove
the guards if a future workload demonstrates auto choosing a harmful generic
plan. A pool-wide force_custom_plan would affect every prepared query and is not
supported by these results.
