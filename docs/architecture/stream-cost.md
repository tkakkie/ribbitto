# Stream cost

What one post costs the delivery loop as open streams grow, measured by
`TestStreamCost` in `internal/realtime/cost_test.go` (#215). The benchmark
measures the current delivery path without HTTP or HTML rendering. The frozen
2026-10-01 results below precede #670's authorization cache and shared epoch
reads; #671 measured those with the same benchmark
([results](../measurements/stream-authorization-results.md)).

## Running it

Run only when asked, never in CI. Export `RIBBITTO_TEST_DATABASE_URL` first
([database tests](../database-tests.md)): the admin URL of a disposable
PostgreSQL on loopback, kept out of the command line.

```sh
RIBBITTO_STREAM_COST=1 go test -count=1 -run '^TestStreamCost$' -v -timeout 30m ./internal/realtime/
```

The server must be loopback, checked before connecting, fallback hosts included.
Loopback alone does not prove a server is disposable: it can be a tunnel.
The benchmark creates its own database from `template0` (`pgtest.NewEmpty`),
migrates and drops only that database; it touches no other database.

Tunables are defined in the `TestStreamCost` comment in
`internal/realtime/cost_test.go`. One correction to that comment: unset
`RIBBITTO_STREAM_COST_POOL` inherits `pgtest.MaxConns` (16), not pgx's default.
A run stops at its first failing step; if all pass, it establishes only a lower bound.
For sidebar measurements, set duration and rate explicitly.

With `_STAGGER`, stream `i` of `N` waits `i × window / N` after its shared
render returns and before `MayReceive`. The wait must stay in a per-stream
renderer wrapper: in the shared loader it would delay one render, not the checks.
It needs `_MEMBERS=distinct` and refuses to start without it, because one
member's streams share one cached allow.

## What is measured

`N` streams in one organisation receive posts through the real delivery path,
with an in-memory sender; the renderer reads messages with their authors but
renders no HTML. Without sidebar placement, all follow one channel.

- **Posting** is an open loop at a fixed rate regardless of latency, with at
  most one second's worth in flight. Missed or incomplete posts fail the step
  as underloaded, rather than silently reducing the offered load.
- **Queries** count attempted statements from posting and delivery over the
  posting window and drain, excluding fixtures. Transaction statements
  (BEGIN, COMMIT, ROLLBACK) are counted apart; empty event reads are a subset
  of queries. Counts are reported per completed post and per delivery.
- **Pool wait** is both the mean per acquisition that found the pool empty
  (`EmptyAcquireWaitTime / EmptyAcquireCount`) and that total spread over
  deliveries. It explains latency, fails no step and is not a ceiling by itself.
- **Delivery latency** is `Post` returning after commit to the in-memory send,
  including any stagger wait, reported as p50/p95/p99. Delivery before `Post`
  returns is a negative sample; this is not a commit timestamp.
- **Authorization** reports epoch reads per post and per post per stream,
  checks per post, cached-allow hit rate and `MayReceive` p50/p95/p99 without
  stagger wait. Epoch reads are a subset of queries. The authorization cache
  lives for the run, so later steps start warm.
- A step fails for underloaded or incomplete posting, p95 over 1 s, or a
  delivery still missing at the deadline (posting time plus 30 s). CPU and
  memory of this process and PostgreSQL are invisible here; watch them outside.

## Sidebar mode (#812)

Initial snapshots must match each member's two count totals before counters
start; initial recounts, unread seeds and read writes are excluded.
Drain requires each member's delivery of every posted sequence and an exact
capped-count snapshot computed after resync publication, with the final
same-snapshot committed sequence. Timing alone cannot prove freshness;
the freshness witness is test-only and supports runs beyond 99 posts.
SQL totals include posting and final resync; elapsed time includes drain.
Message and unread delivery use the post-return clock above; final resync
runs from publication until every stream is exact, including outstanding delivery.
The delivery denominator includes both frame kinds. Pool wait totals sum
acquisition waits, not wall time.

[Client counts results](../measurements/stream-client-counts-results.md) holds
placement rules, commands and measured results;
[sidebar counts history](../measurements/sidebar-counts-history.md) summarises
the stopped server-recount comparisons.

## Results, 2026-10-01

Apple M-series laptop, darwin/arm64, 10 CPUs, Go 1.27.1; PostgreSQL 18 in
Docker on the same machine; pool 10 connections; batch 100; 10 posts/s for
10 s. Every step completed all 100 scheduled posts. These are what this
setup measured that day, not today's performance or an absolute capacity;
the coarse step ladder only brackets the limit between a pass and a failure.

**First measurement (no shared reads, 1 to 1,000 streams):**
`3c981fe185bcb435bd6050abe13e6fd6af60f581`; #228's author identifies this
measurement SHA, before the timeout bug.
**Pre-deletion SHA (both tables):**
`5fd630c8abcc7bf114674883ed1d02c1e70484fb`; retrieve the tables and past
commands with `git show 5fd630c8abcc7bf114674883ed1d02c1e70484fb:docs/architecture/stream-cost.md`.

Without shared event reads and message renders, **300 streams passed / 500
failed** (p95 82 ms / 2.34 s). Each delivery cost about five queries: event
read, membership check, message and its two author lookups; plus two
transaction statements, and three queries per post. At 10 posts/s, the
300-stream step needed about 15,000 queries/s; at 500 the pool saturated.
No event read returned empty. Lagging streams batched events, lowering
queries per delivery; caught-up streams read one new event per post.

## With shared reads (#227), 2026-10-01

Same hardware, versions, pool, batch, rate and duration; `_CACHE=1` shares
**event reads and message renders**, not #670's later authorization cache.
**Shared-read re-run SHA:** `b33c82efbb298d60318fbf1237299e3e8f831b1d`
(#233's author comment: after the joiner-freshness fix). Both SHAs are
PR-branch commits. The no-shared-reads comparison's commit is unidentified
beyond #233's branch on 2026-10-01.

Without shared reads the comparison again measured **300 pass / 500 fail**
(p95 83 ms / 2.55 s). With them, **2,000 passed / 3,000 failed**, both with
one member (p95 89 ms / 2.93 s) and distinct members (90 ms / 2.44 s).
The ladder stopped at 3,000; 5,000 was not run. Every step completed 100 posts.
These pairs bracket this setup's measured limit that day, not a capacity promise.

A post cost about `N + 10` queries: one membership check per stream, three
posting queries, one shared message read with two author lookups, and about
four event reads. Distinct members changed nothing, indicating sharing per
organisation. Only full event batches are retained
([consistency rules](stream-limits.md)); short batches can require repeated reads.
The remaining per-stream query was the membership check, which saturated
this setup's pool beyond the 2,000-stream passing step.

## What it points to

Under this load the pool saturated while the loop's goroutines mostly waited
between posts: the evidence points at per-connection database work, but does
not prove Go adds nothing at higher counts. #670's authorization cache follows
its own [freshness protocol](stream-authorization.md#decision): cached allows
remove the per-stream query; a cache miss still loads one membership per stream.
Its later costs and limits are in [authorization results](../measurements/stream-authorization-results.md);
end-to-end conditions and figures are in [load results](../measurements/load-results.md).
