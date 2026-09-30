# Stream cost

What one post costs the delivery loop as open streams grow, measured by
`TestStreamCost` in `internal/realtime/cost_test.go` (#215). It measures the
design M3 ships — every connection reads `event_log`, re-authorizes and reads
the message itself — so that a faster design can be compared with the same
benchmark.

## Running it

It runs only when asked and never in CI:

```sh
RIBBITTO_STREAM_COST=1 RIBBITTO_TEST_DATABASE_URL=postgres://…@127.0.0.1:…/postgres \
  go test -run 'TestStreamCost$' -v -timeout 30m ./internal/realtime/
```

`RIBBITTO_STREAM_COST_STEPS` (streams per step, default `1,10,100,1000,5000`),
`_RATE` (posts per second, default 10), `_DURATION` (posting time per step,
default `10s`) and `_POOL` (pool size) tune it. The server must be loopback,
checked before connecting, fallback hosts included; the benchmark works in a
database `pgtest` creates and drops. Loopback alone does not prove a server
is disposable: it can be a tunnel.

## What is measured

Per step: `N` streams of one member in one organisation, all following one
channel, run the real loop (#209), event reader (#208), `authz.MayReceive` and
one-message read (#206); the renderer reads the message but renders no HTML.
Posts go through `message.Service` with the hub as notifier.

- **Queries** come from `postgres.QueryCounter` over the posting window and
  the drain, fixtures excluded; transaction statements (BEGIN, COMMIT,
  ROLLBACK) are counted apart, and so are reads that returned no events.
- **Mean acquire wait** is `pgxpool`'s acquire duration per acquisition.
- **Latency** runs from `Post` returning after commit to the in-memory send
  (a delivery before `Post` returns counts as a negative sample). It is not a
  commit timestamp.
- A step fails when p95 exceeds 1 s, when the mean acquire wait exceeds half
  of p95, or when a delivery is still missing 30 s after posting stops.

## Results, 2026-10-01

Apple M-series, darwin/arm64, 10 CPUs, Go 1.27.1; PostgreSQL 18 in Docker on
the same machine; pool 10 connections; batch 100; 10 posts/s for 10 s.

| Streams | Queries/post | Transaction statements/post | Queries/delivery | Mean acquire wait | p50 | p95 | Result |
|---|---|---|---|---|---|---|---|
| 1 | 8.0 | 4.0 | 8.00 | 0 | 2.5 ms | 4.3 ms | pass |
| 10 | 53 | 22 | 5.30 | 1 µs | 5.8 ms | 7.3 ms | pass |
| 100 | 503 | 202 | 5.03 | 6.2 ms | 22 ms | 32 ms | pass |
| 200 | 1,003 | 402 | 5.01 | 11.6 ms | 38 ms | 55 ms | pass |
| 300 | 1,503 | 602 | 5.01 | 17.3 ms | 55 ms | 81 ms | pass |
| 500 | 2,090 | 1,002 | 4.18 | 54 ms | 1.27 s | 2.15 s | fail: p95 over 1 s |
| 1,000 | 4,089 | 2,002 | 4.09 | 117 ms | 5.2 s | 9.9 s | fail: p95 over 1 s |

No read came back empty. **Highest passing step: 300 streams; first failing:
500.** Each delivery costs about five queries — one event read, one
membership check, and the message with its two author lookups in a
transaction — plus the post's own three. At 10 posts/s, 300 streams need
about 15,000 queries/s and pass; 500 need 25,000 and saturate the pool, which
also slows posting (the 1,000-stream step managed 82 of 100 posts). Below 1
query per delivery, reads batch several events when a stream lags.

## What it points to

The ceiling is set by per-connection database work, not by Go: the loop's
goroutines are idle between posts. A shared reader per organisation that
reads each event and the message once, renders once per language, and fans
out in memory — authorization still per connection, replay still on the
database path — would make the per-post cost independent of the number of
streams (#227).
