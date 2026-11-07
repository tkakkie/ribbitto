# Recount capacity

## Application pool sizing

For a VPS with about 2 vCPUs and 4 GB RAM running both ribbitto and PostgreSQL,
start with `pool_max_conns=8` in `RIBBITTO_DATABASE_URL`. This is an initial
setting, not a measured capacity guarantee: it allows four simultaneous count snapshots
and leaves four connections for posting, reads without counts and background work.
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
[load results](measurements/load-results.md) used different hardware and do not
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
- Conversation page counts and sidebar recounts locally set a one-minute
  statement budget unless the URL sets `statement_timeout`. Count candidates
  are capped, but fragmented read sets and above-floor filtering can still scan
  history. This also covers the same count work on ordinary pages. One minute
  equals the 60 s HTTP write timeout: it is deliberately the ceiling, so a
  pathological count can run up to the write deadline. Bounded `One`, `Many`,
  `Locate`, pins-only and member-list snapshots keep the session budget without
  an extra statement.
- Unread's `Reading.Feed` and `Reading.Topic` use `InTxDetachedCommit` for bounded
  mark-as-read writes, not recounts. Their bounded probes (`FirstMessageAfter`,
  `HasMessages` and topic candidates) need no extension of the 30 s default.
- Retention's initial organisation listing scans the whole event log, so only
  that read snapshot locally sets one minute unless the URL sets
  `statement_timeout`. Each subsequent transaction processes at most 1,000
  indexed rows and keeps the 30 s statement and 5 s lock
  defaults. The worker's existing one-minute context bounds the entire run;
  errors preserve committed batches for the next tick.
- `cmd/seed` commits each use case separately, rather than bulk-loading in one
  statement. Its expected-message export reads history in 50-message pages and
  skips unread counts, so those snapshots keep the session budget.
  The overall command can exceed 30 s without any one write statement needing
  longer; no seed-specific override is needed.

The one-minute read budgets use `SET LOCAL statement_timeout` only when ribbitto
supplied the session default. Any explicit URL value, including `0` and values
in `options`, skips the local setting. Local settings reset on commit or rollback;
other operations retain the configured session defaults.

## Current computation sizing (#1046)

One process-wide `RecountLimit` covers page counts and SSE connect/read/resync/
move recounts: `max(1, floor(C/2))` slots for pgx pool maximum C
(URL/defaults). `ceil(C/2)` connections remain for work without counts if C >= 2;
C=1 has no headroom. Stream registrations are separate.

SSE waits 25 ms, then retains dirty/watch state and retries after 250–1,000 ms
jitter, without 503. Connect delays 0–3 s hold no slot; durable/owner frames and
authorization continue. Slots release only when computation returns.

Before snapshots, pages take one concurrent slot per organisation/member,
then a shared slot with a 25 ms wait. Over-cap returns 429
immediately; shared exhaustion returns 503. One slot (pool of two) means other
members get 503 while held: expected. Without a hub, only the member cap applies.
Coverage: GET/HEAD channel/topic, older/around, members/overview/list pages;
POST creation errors, non-htmx post/branch errors and invalid images on both
overview routes. Paths without counts (successes, redirects, htmx composer/branch
fragments) take no slot. Both set `Retry-After: 1` (seconds, a hint):
localized navigation pages, empty HEAD, status only otherwise; POST follows the
[form rule](architecture/rate-limits.md#member-rejection-responses).
