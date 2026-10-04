# Stream cost

What one post costs the delivery loop as open streams grow, measured by
`TestStreamCost` in `internal/realtime/cost_test.go` (#215). It measures the
design M3 ships — every connection reads `event_log`, re-authorizes and reads
the message itself — so that a faster design can be compared with the same
benchmark.

## Running it

It runs only when asked and never in CI. Export `RIBBITTO_TEST_DATABASE_URL`
first, as for `make check` ([`database.md`](../database.md)): the admin URL of a
disposable PostgreSQL on loopback, kept out of the command line. The two runs
below then used:

```sh
# Run A: the default steps
RIBBITTO_STREAM_COST=1 RIBBITTO_STREAM_COST_POOL=10 \
  go test -count=1 -run 'TestStreamCost$' -v -timeout 30m ./internal/realtime/
# Run B: between run A's last pass and first failure
RIBBITTO_STREAM_COST=1 RIBBITTO_STREAM_COST_POOL=10 RIBBITTO_STREAM_COST_STEPS=200,300,500 \
  go test -count=1 -run 'TestStreamCost$' -v -timeout 30m ./internal/realtime/
```

`RIBBITTO_STREAM_COST_STEPS` (streams per step, default `1,10,100,1000,5000`;
a run stops at its first failing step), `_RATE` (posts per second, default
10), `_DURATION` (posting time per step, default `10s`), `_POOL` (pool size,
default pgx's), `_CACHE=1` (#227's shared reads) and `_MEMBERS=distinct`
(one member per stream) tune it. The server must be loopback, checked before
connecting, fallback hosts included. The benchmark works in a database it
creates from `template0` (`pgtest.NewEmpty`), migrates itself and drops
afterwards; it touches no other database. Loopback alone does not prove a
server is disposable: it can be a tunnel.

## What is measured

Per step: `N` streams of one member in one organisation, all following one
channel, run the real loop (#209), event reader (#208), `org.Authorizer.MayReceive` and
one-message read (#206); the renderer reads the message but renders no HTML.
Posts go through `message.Service` with the hub as notifier.

- **Posting** is an open loop: posts are scheduled at the fixed rate
  whatever their latency, with at most one second's worth in flight. A post
  that would exceed that is missed, and a step with a missed or incomplete
  post fails as underloaded rather than quietly offering less load.
- **Queries** are statements attempted, counted by `postgres.QueryCounter`
  over the posting window and the drain, fixtures excluded; they include
  both posting and delivery. Transaction statements (BEGIN, COMMIT,
  ROLLBACK) are counted apart from them. Empty reads (event reads that
  returned nothing) are a subset of the queries. Each is reported per
  completed post and per delivery.
- **Pool wait** is reported two ways: per acquisition that found the pool
  empty (`EmptyAcquireWaitTime / EmptyAcquireCount`) and spread over the
  deliveries (what the pool adds to a delivery on average). It explains
  latency but fails no step (#227): with the caches, waits become rare and
  their per-acquisition mean can exceed half of a 5 ms p95, and without them
  the pool dominates a 30 ms p95 at 100 streams — neither is a ceiling.
- **Latency** runs from `Post` returning after commit to the in-memory send
  (a delivery before `Post` returns counts as a negative sample). It is not a
  commit timestamp.
- A step fails when it is underloaded or incomplete, when p95 exceeds 1 s,
  or when a delivery is still missing when the step's deadline (posting
  time plus 30 s) passes.

## Results, 2026-10-01

Apple M-series, darwin/arm64, 10 CPUs, Go 1.27.1; PostgreSQL 18 in Docker on
the same machine; pool 10 connections; batch 100; 10 posts/s for 10 s. Every
step completed all 100 scheduled posts.

| Run | Streams | Queries/post | Transaction statements/post | Queries/delivery | Mean empty-acquire wait | p50 | p95 | Result |
|---|---|---|---|---|---|---|---|---|
| A | 1 | 8.0 | 4.0 | 8.00 | 0 | 3.5 ms | 4.3 ms | pass |
| A | 10 | 53 | 22 | 5.30 | 0 | 5.7 ms | 7.4 ms | pass |
| A | 100 | 503 | 202 | 5.03 | 6.6 ms | 22 ms | 32 ms | pass |
| B | 200 | 1,003 | 402 | 5.01 | 11.9 ms | 38 ms | 56 ms | pass |
| B | 300 | 1,503 | 602 | 5.01 | 17.7 ms | 56 ms | 82 ms | pass |
| B | 500 | 2,089 | 1,002 | 4.18 | 55 ms | 1.35 s | 2.34 s | fail: p95 over 1 s |
| A | 1,000 | 4,068 | 2,002 | 4.07 | 115 ms | 7.2 s | 13.4 s | fail: p95 over 1 s |

No event read came back empty. **Highest passing step: 300 streams; first
failing: 500.** Each delivery costs about five queries — one event read, one
membership check, and the message with its two author lookups — and two
transaction statements (the one-message read's BEGIN and COMMIT), plus the
post's own three queries. At 10 posts/s, 300 streams need about 15,000
queries/s and pass; 500 need 25,000 and the pool saturates. Below five
queries per delivery, reads batch several events when a stream lags.

## With shared reads (#227), 2026-10-01

Same machine and settings, run with and without `_CACHE=1` (steps
`100,200,300,500` without it; `100,300,500,1000,2000,3000,5000` with it, once
with one member and once with `_MEMBERS=distinct`):

| Case | Streams | Queries/post | Queries/delivery | Pool wait/delivery | p50 | p95 | Result |
|---|---|---|---|---|---|---|---|
| no cache | 300 | 1,503 | 5.01 | 53 ms | 56 ms | 83 ms | pass |
| no cache | 500 | 2,087 | 4.17 | 123 ms | 1.29 s | 2.55 s | fail: p95 over 1 s |
| cache, one member | 1,000 | 1,010 | 1.01 | 21 ms | 39 ms | 48 ms | pass |
| cache, one member | 2,000 | 2,010 | 1.01 | 40 ms | 81 ms | 89 ms | pass |
| cache, one member | 3,000 | 3,007 | 1.00 | 73 ms | 1.56 s | 2.93 s | fail: p95 over 1 s |
| cache, distinct members | 1,000 | 1,010 | 1.01 | 22 ms | 40 ms | 52 ms | pass |
| cache, distinct members | 2,000 | 2,010 | 1.01 | 41 ms | 81 ms | 90 ms | pass |
| cache, distinct members | 3,000 | 3,007 | 1.00 | 69 ms | 1.40 s | 2.44 s | fail: p95 over 1 s |

Every step completed its 100 posts. With the caches a post costs about
`N + 10` queries — one membership check per stream, plus the post's three,
one shared message read with its two author lookups, and about four event
reads — and **the highest passing step rises from 300
to 2,000 streams**. Distinct members change nothing, so the sharing is per
organisation, not per member. The cache stores only full event batches: a
short batch, such as the one holding a new post, is never stored, and the
streams that joined a read in flight get a second read started after it
finished, so none gets a snapshot older than its own call; a stream that
reads later repeats it.
The remaining per-stream query is the membership check (#231); beyond about
2,000 streams on this machine it saturates the pool.

## What it points to

Under this load the pool saturates while the loop's goroutines mostly wait
between posts: the evidence points at per-connection database work, though
it does not prove Go adds nothing at higher counts. #227 shares each event read
(plus one private read for a short batch's joiners) and message read per
organisation and renders once per language; what
remains per connection is the membership check, until it has a freshness
protocol of its own (#231). Whether a shared reader per organisation (#232)
is worth building is decided on these numbers.
