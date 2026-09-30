# Stream cost

What one post costs the delivery loop as open streams grow, measured by
`TestStreamCost` in `internal/realtime/cost_test.go` (#215). It measures the
design M3 ships — every connection reads `event_log`, re-authorizes and reads
the message itself — so that a faster design can be compared with the same
benchmark.

## Running it

It runs only when asked and never in CI. The two runs below used:

```sh
# Run A: the default steps
RIBBITTO_STREAM_COST=1 RIBBITTO_STREAM_COST_POOL=10 \
  RIBBITTO_TEST_DATABASE_URL=postgres://…@127.0.0.1:…/postgres \
  go test -count=1 -run 'TestStreamCost$' -v -timeout 30m ./internal/realtime/
# Run B: between run A's last pass and first failure
RIBBITTO_STREAM_COST=1 RIBBITTO_STREAM_COST_POOL=10 RIBBITTO_STREAM_COST_STEPS=200,300,500 \
  RIBBITTO_TEST_DATABASE_URL=postgres://…@127.0.0.1:…/postgres \
  go test -count=1 -run 'TestStreamCost$' -v -timeout 30m ./internal/realtime/
```

`RIBBITTO_STREAM_COST_STEPS` (streams per step, default `1,10,100,1000,5000`;
a run stops at its first failing step), `_RATE` (posts per second, default
10), `_DURATION` (posting time per step, default `10s`) and `_POOL` (pool
size, default pgx's) tune it. The server must be loopback, checked before
connecting, fallback hosts included. The benchmark works in a database it
creates from `template0` (`pgtest.NewEmpty`), migrates itself and drops
afterwards; it touches no other database. Loopback alone does not prove a
server is disposable: it can be a tunnel.

## What is measured

Per step: `N` streams of one member in one organisation, all following one
channel, run the real loop (#209), event reader (#208), `authz.MayReceive` and
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
- **Mean empty-acquire wait** is `pgxpool`'s wait per acquisition that
  found the pool empty (`EmptyAcquireWaitTime / EmptyAcquireCount`).
- **Latency** runs from `Post` returning after commit to the in-memory send
  (a delivery before `Post` returns counts as a negative sample). It is not a
  commit timestamp.
- A step fails when it is underloaded, when p95 exceeds 1 s, when the mean
  empty-acquire wait exceeds half of p95, or when a delivery is still
  missing 30 s after posting stops.

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

## What it points to

Under this load the pool saturates while the loop's goroutines mostly wait
between posts: the evidence points at per-connection database work, though
it does not prove Go adds nothing at higher counts. Reading each event and
its message once per organisation, and rendering once, instead of once per
connection, would remove most of it; authorization stays a query per
connection until it has a freshness protocol of its own (#227).
