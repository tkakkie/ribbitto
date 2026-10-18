# Message paging plans

Measured 2026-10-18 for #1300 at `406e1121b6cabb64c171c6cd83444c8d66a8deb8` with
the unchanged [ListMessagesBefore and
ListMessagesAfter](../db/queries/conversation/message.sql). **Recommendation: no
change needed for the measured workload.** Forced generic plans lose useful
index conditions, but auto mode retained custom plans in every case below. Keep
the queries and pool configuration. The maintainer records the disposition;
other workloads remain untested.

## Method and reproduction

Measured SQL in the Codex sandbox using a disposable test database. PostgreSQL
was **18.6**, Debian 18.6-1.pgdg13+2, Linux aarch64 (GCC 14.2.0). Settings:
shared_buffers 128 MiB, work_mem 4 MiB, effective_cache_size 4 GiB,
random_page_cost 4, jit on, max_parallel_workers_per_gather 2. Only
plan_cache_mode changed. Goose applied all migrations through 00019.

The busy channel has 1,000,000 messages, including 200 sparse-topic messages,
one every 5,000 messages. Another organisation/channel has 100,000 messages.
Bodies are about 150 characters; quote/pin fields are NULL. Sequences 1–2
precede the messages. The boundary models historical events past retention,
without backfill or disabled triggers. Each organisation has one member; no
concurrent writes.

`psql` was unavailable in the sandbox. A temporary Go driver used pgx v5.11.0's
simple protocol to issue SQL, and goose v3.29.0 to apply the real migrations.
Each phase used a separate connection. Equivalent psql SQL follows; use only a
throwaway database. From the repository root:

```sh
psql '<local test database>' -v ON_ERROR_STOP=1 \
  -c 'CREATE DATABASE ribbitto_1300_paging'
goose -dir db/migrations postgres '<throwaway database>' up
psql '<throwaway database>' -v ON_ERROR_STOP=1
```

Seed the throwaway database; the account hash is an unusable fixture:

```sql
CREATE TEMP TABLE fixture AS
SELECT md5('org' || g)::uuid AS org, md5('channel' || g)::uuid AS channel,
       md5('member' || g)::uuid AS member, md5('main' || g)::uuid AS main,
       md5('sparse' || g)::uuid AS sparse,
       CASE g WHEN 1 THEN 1000000 ELSE 100000 END AS n
FROM generate_series(1, 2) AS g;
BEGIN;
INSERT INTO organization (id, slug, name, event_seq, event_log_boundary_seq)
SELECT org, 'paging-' || n, 'Paging measurement', n + 2, n + 2 FROM fixture;
INSERT INTO account (id, email, display_name, password_hash)
SELECT member, n || '@example.invalid', 'Measurement', '$argon2id$fixture' FROM fixture;
INSERT INTO member (id, organization_id, account_id, role, joined_event_seq, handle)
SELECT member, org, member, 'owner', 1, 'measurement' FROM fixture;
INSERT INTO channel (id, organization_id, name, is_default, default_topic_id)
SELECT channel, org, 'Busy channel', true, main FROM fixture;
INSERT INTO topic (id, organization_id, channel_id, is_default)
SELECT main, org, channel, true FROM fixture;
INSERT INTO topic (id, organization_id, channel_id, name)
SELECT sparse, org, channel, 'Sparse topic' FROM fixture WHERE n = 1000000;
COMMIT;
INSERT INTO message (organization_id, channel_id, topic_id, member_id, body,
                     format, event_seq, created_at)
SELECT org, channel,
       CASE WHEN n = 1000000 AND i % 5000 = 0 THEN sparse ELSE main END,
       member, repeat('Message text for paging measurement. ', 4) || i,
       'chat_v1', i + 2, timestamptz '2026-01-01 00:00:00+00' + i * interval '1 second'
FROM fixture CROSS JOIN LATERAL generate_series(1, n) AS i;
ANALYZE;
```

These are the parameterised queries, using the source's `SELECT *` (the driver
used sqlc's equivalent explicit column expansion). Parameters: organisation,
channel, nullable topic, cursor, limit. First-page means a NULL before cursor.
Limits match history's 51 rows (including the older-page sentinel) and
around-message's 25 newer rows.

```sql
CREATE TEMP TABLE queries (name text, sql text);
INSERT INTO queries VALUES ('b', $q$
SELECT * FROM message
WHERE organization_id = $1 AND channel_id = $2
  AND ($3::uuid IS NULL OR topic_id = $3::uuid)
  AND ($4::bigint IS NULL OR event_seq < $4::bigint)
ORDER BY event_seq DESC LIMIT $5
$q$), ('a', $q$
SELECT * FROM message
WHERE organization_id = $1 AND channel_id = $2
  AND ($3::uuid IS NULL OR topic_id = $3::uuid)
  AND event_seq > $4::bigint
ORDER BY event_seq LIMIT $5
$q$);
CREATE TEMP TABLE cases
  (label text, name text, pos int, topic text, cursor text, lim int);
INSERT INTO cases VALUES
('feed-first',   'b', 0, 'NULL', 'NULL', 51),
('feed-before',  'b', 1, 'NULL', '500003', 51),
('topic-first',  'b', 2, 'md5(''sparse1'')::uuid', 'NULL', 51),
('topic-before', 'b', 3, 'md5(''sparse1'')::uuid', '500003', 51),
('feed-after',   'a', 0, 'NULL', '500002', 25),
('topic-after',  'a', 1, 'md5(''sparse1'')::uuid', '500002', 25);
CREATE TEMP VIEW executions AS
SELECT label, name, pos, format(
  'EXECUTE %I(md5(''org1'')::uuid,md5(''channel1'')::uuid,%s,%s,%s)',
  name, topic, cursor, lim) AS sql FROM cases;
```

Phase A: generic shape, shared by all parameter values. **Do not combine
GENERIC_PLAN with ANALYZE.**

```sql
SELECT 'EXPLAIN (GENERIC_PLAN, FORMAT JSON) ' || sql FROM queries ORDER BY name
\gexec
```

Phase B: prepare, force each mode, and execute the same parameters. For each
case, discard repetition 0 and retain repetitions 1–5, alternating generic then
custom. The working set exceeds shared_buffers; scans evict pages despite
warm-up. No cache flushes were used.

```sql
SELECT format('PREPARE %I(uuid,uuid,uuid,bigint,bigint) AS %s', name, sql)
FROM queries
\gexec
SELECT format('SET plan_cache_mode = %s; SELECT %L;
 EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) %s;', mode, label || ' ' || r, sql)
FROM executions CROSS JOIN generate_series(0,5) AS r
CROSS JOIN (VALUES ('force_generic_plan'), ('force_custom_plan')) AS modes(mode)
ORDER BY r, name DESC, pos, mode DESC
\gexec
```

Phase C: reset prepared statements for each isolated case under auto. Each
EXPLAIN ANALYZE counts as one execution. Inspect counters at 5, 6 and 15 and
plans from 6 onward:

```sql
SET plan_cache_mode = auto;
SELECT format('DEALLOCATE ALL; PREPARE %I(uuid,uuid,uuid,bigint,bigint) AS %s;',
              e.name, q.sql) || string_agg(format(
 'EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) %s;
 SELECT %L, name, generic_plans, custom_plans
 FROM pg_prepared_statements WHERE name = %L;',
 e.sql, e.label || ' ' || r, e.name), E'\n' ORDER BY r)
FROM executions e JOIN queries q USING (name)
CROSS JOIN generate_series(1,15) AS r
GROUP BY e.label, e.name, q.sql
\gexec
```

Mixed phase: cycle through four before and two after cases, 20 executions each:

```sql
DEALLOCATE ALL;
SELECT format('PREPARE %I(uuid,uuid,uuid,bigint,bigint) AS %s', name, sql)
FROM queries
\gexec
SELECT format('EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) %s;
 SELECT %L, name, generic_plans, custom_plans
 FROM pg_prepared_statements WHERE name = %L;',
 sql, label || ' ' || r, name)
FROM generate_series(1,20) AS r JOIN executions
ON pos = (r-1) % CASE name WHEN 'b' THEN 4 ELSE 2 END
ORDER BY r, name DESC
\gexec
```

Afterwards drop the measurement database. The database and temporary driver, SQL
and raw outputs were removed after this run.

```psql
\connect '<local test database>'
DROP DATABASE ribbitto_1300_paging;
```

## Results

All plans were Limit → Index Scan, without sorting, one scan loop. Real indexes:

| Code | Index | Keys |
|---|---|---|
| C | message_channel_event_seq_idx | organization_id, channel_id, event_seq |
| T | message_topic_event_seq_idx | organization_id, topic_id, event_seq |
| U | message_organization_id_event_seq_key | organization_id, event_seq (unique) |

O, Cn, Tn and S mean organisation, channel, topic and sequence predicates.
Generic shapes matched EXPLAIN GENERIC_PLAN and forced EXECUTE; custom shapes
came from EXECUTE.

| Statement / parameters | Generic index conditions; filter | Custom index conditions; filter |
|---|---|---|
| Before / feed first | C: O, Cn; nullable topic and cursor guards | U: O; Cn |
| Before / feed before | C: O, Cn; nullable topic and cursor guards | U: O, S < cursor; Cn |
| Before / topic first | C: O, Cn; nullable topic and cursor guards | T: O, Tn; Cn |
| Before / topic before | C: O, Cn; nullable topic and cursor guards | T: O, Tn, S < cursor; Cn |
| After / feed | C: O, Cn, S > cursor; nullable topic guard | U: O, S > cursor; Cn |
| After / topic | C: O, Cn, S > cursor; nullable topic guard | T: O, Tn, S > cursor; Cn |

Custom feed plans chose U rather than C with this one-channel-per-organisation
distribution; both indexes remained available. Generic before cannot seek the
cursor or topic; after retains its unguarded sequence condition but cannot seek
the topic.

Times are median EXPLAIN **Execution Time** in ms over five retained runs
(planning excluded). Buffers are shared hits/reads at Limit, and filter removals
are at Index Scan, from the median-time execution, without summing repeats or
parent/child nodes. Reads need not mean disk I/O. G/Cu mean forced
generic/custom. Every before run returned 51 rows; every after run returned 25.

| Form | Mode | Index | Time ms | Shared hit/read | Rows removed by filter |
|---|---|---|---:|---:|---:|
| Feed first | G | C | 0.031 | 1 / 5 | 0 |
| Feed first | Cu | U | 0.020 | 4 / 2 | 0 |
| Feed before | G | C | 62.545 | 9 / 22,087 | 500,000 |
| Feed before | Cu | U | 0.026 | 4 / 3 | 0 |
| Topic first | G | C | 29.089 | 6 / 11,043 | 249,950 |
| Topic first | Cu | T | 0.058 | 51 / 3 | 0 |
| Topic before | G | C | 74.125 | 11,048 / 22,091 | 749,950 |
| Topic before | Cu | T | 0.062 | 51 / 3 | 0 |
| Feed after | G | C | 0.019 | 3 / 2 | 0 |
| Feed after | Cu | U | 0.018 | 2 / 3 | 0 |
| Topic after | G | C | 8.630 | 5,323 / 204 | 124,975 |
| Topic after | Cu | T | 0.026 | 28 / 0 | 0 |

The generic feed-before range was 60.615–107.468 ms; topic-before was
73.145–74.864 ms. Absolute timings depend on cache state and this test server;
filter counts and missing index conditions explain the extra work independently.

Under **auto on PostgreSQL 18.6**, all six isolated cases had these counters
(generic/custom): execution 5 = **0/5**, 6 = **0/6**, 15 = **0/15**. Both mixed
statements had 0/5, 0/6 and **0/20**. Plans on execution 6 and later retained
the custom shapes above. Five custom executions therefore did not imply a
switch. Generic estimated total Limit costs were 15,647.85 (before) and
14,524.63 (after), versus 10.08–199.27 for custom plans with these parameters;
the unknown limit also contributes to generic estimates. Median forced custom
planning time was 0.034–0.114 ms across cases.

The forced-plan hazard is confirmed, but the suspected default-cache regression
was not reproduced. Splitting feed/topic and first/before queries would remove
the guards if a future workload demonstrates auto choosing a harmful generic
plan. A pool-wide force_custom_plan would affect every prepared query and is not
supported by these results.
