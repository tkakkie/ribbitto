# Recount capacity

## Application pool sizing

For a VPS with about 2 vCPUs and 4 GB RAM running both ribbitto and PostgreSQL,
start with `pool_max_conns=8` in `RIBBITTO_DATABASE_URL`. This is an initial
setting, not a measured capacity guarantee: it allows four simultaneous recounts
and leaves four connections for posting, page reads and background work.
Without an override, pgx uses `max(4, NumCPU)`: on two CPUs that means four
connections and two recounts. The application does not change pgx's pool size.

Eight gives short I/O waits some overlap without starting dozens of competing
queries on two CPUs. More connections can increase CPU contention and memory
pressure. PostgreSQL starts a backend for each connection; backend memory and
query working memory (including `work_mem` per sort/hash operation, potentially
several per query) add up. Leave room in PostgreSQL's `max_connections` for all
application processes, migrations, maintenance and administration, including its
reserved connections; the pool maximum is per process, not per installation.

Adjust using pool acquisition waits, query latency, PostgreSQL CPU and memory,
and the actual workload. Check slow queries, indexes, storage and recount work
before raising the maximum. Larger pools are only one lever; the historical
[load results](architecture/load-results.md) used different hardware and do not
make their 40-connection setting a small-VPS recommendation.

## Server-side timeouts

`internal/platform/postgres.OpenPool` supplies these session defaults only when
the parsed database configuration does not already set them:

| Setting | Default | Reason |
|---|---|---|
| `statement_timeout` | `30s` | Stop a slow statement before the ordinary 60 s HTTP write budget expires. |
| `lock_timeout` | `5s` | Fail contended writes sooner, rather than occupy connections for the full statement budget. |
| `idle_in_transaction_session_timeout` | `60s` | Release abandoned transactions and their locks, allowing short gaps between statements. |

These are initial operational budgets, not latency measurements. PostgreSQL
enforces them independently of HTTP: `http.Server.WriteTimeout` does not cancel
the request context. The statement limit applies to each statement, not to the
whole request or transaction; the idle limit applies only between statements
inside a transaction, not to an idle pool connection or an SSE stream.

Append URL query parameters, for example
`?pool_max_conns=8&statement_timeout=45s&lock_timeout=2s&idle_in_transaction_session_timeout=90s`
(use `&` if the URL already has a query). Explicit values, including `0` to
disable a timeout, are preserved. pgx leaves `options` intact; settings there
also suppress the corresponding default, for example
`?options=-c%20statement_timeout%3D45s`. Encode spaces as `%20`, not `+`.
Changes apply to newly opened pools. Migrations use `postgres.Open` and are
unaffected by these `OpenPool` defaults.

Long-running paths use the following budgets:

- Stream replay reads indexed, organisation-scoped batches (normally 100 events)
  in separate snapshots. Catch-up across many batches does not extend one
  statement; replay keeps the 30 s default.
- Conversation read snapshots, including page counts and sidebar recounts,
  locally set a one-minute statement budget. Count candidates are capped, but
  fragmented read sets and above-floor filtering can still scan history. This
  also covers the same count work on ordinary pages. A pathological query still
  fails after one minute; this is not an unlimited recount exemption.
- Retention's initial organisation listing scans the whole event log, so only
  that read snapshot locally sets one minute. Each subsequent transaction
  processes at most 1,000 indexed rows and keeps the 30 s statement and 5 s lock
  defaults. The worker's existing one-minute context bounds the entire run;
  errors preserve committed batches for the next tick.
- `cmd/seed` commits each use case separately, rather than bulk-loading in one
  statement. Its expected-message export reads history in 50-message pages and
  skips unread counts (it shares the conversation reader's one-minute snapshots).
  The overall command can exceed 30 s without any one write statement needing
  longer; no seed-specific override is needed.

The one-minute read budgets use `SET LOCAL statement_timeout`, including when the URL
sets another session default. They reset on commit or rollback. Other pool
operations retain the configured session defaults.

## Current computation sizing (#1046)

One process-wide `RecountLimit` covers connect, read, resync and move recounts,
separately from stream registrations. It is sized as `max(1, floor(C/2))`, where
C is the effective pgx pool maximum (including the database URL's
`pool_max_conns` and pgx defaults). This reserves `ceil(C/2)` connections for
normal work when C >= 2. A one-connection pool permits one recount and cannot
reserve a separate normal connection; deployments needing headroom must provide
at least two connections. No separate configuration is required. Startup logs
the effective pool maximum, computation limit and stream registration limit.

Slot acquisition waits at most 25 ms. Expiry defers the computation: dirty state
and watch coverage remain pending, and a new attempt follows after 250–1,000 ms
of jitter. Slots are released only when the actual computation returns.
Established streams do not return 503 on deferral.

Connect sets a recount deadline randomly within 0–3 s. Durable frames, owner
frames and authorization continue during that delay; no computation slot is
occupied until the deadline. This bounds sidebar computation only: ordinary
page loads retain their exact SQL and gain no admission limit.
