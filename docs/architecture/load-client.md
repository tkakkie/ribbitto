# Development load client

With [seed credentials](../seed-data.md) on a disposable
machine and `make dev` running, use:
```sh
go run ./cmd/loadgen -tokens /tmp/loadtest.json -streams 8 -duration 5s -rate 2
```
For loopback Caddy, add `-target https://localhost:8443 -ca /path/to/root.crt`.
Only that PEM pool is trusted; HTTP uses HTTP/1.1, HTTPS negotiates HTTP/2.
The dialer checks every resolved address; proxies and origin changes are refused.
Limits: 10 minutes, 100,000 stream attempts, 100 posts/s.
Accounts and their sessions rotate; all traffic uses the first fixture channel.
Each invocation runs one step. Unless `-cursor` overrides Last-Event-ID (empty
exercises HTTP 400), the first token reads the page's `events?after=` cursor once.
`-dial-concurrency` (default 64, 1–100000) bounds concurrent stream attempts;
`-setup` (default 30s, >0 through 5m) counts unfinished attempts as failures.
Observation starts after setup. Posts follow a fixed schedule with at most one
second's worth in flight; overloaded slots are missed. Each POST has a 10s
deadline. After observation all POSTs settle, then `-drain` (default 30s, >0
through 5m) waits for all established streams to receive every answered-200 post.
Early deliveries count; duplicate markers within one payload count only once.
Stdout is one JSON object of numeric parameters, counts, durations (seconds),
latencies (milliseconds) and reason flags, plus `Verdict`. `AchievedRate` is
sent posts per observation second. No credentials or
content are emitted. `underloaded` takes precedence for missed/unsent posts;
otherwise `fail` means p95 >1s, missing delivery or any request/stream failure
(including 429, 503 and reset); otherwise `pass`. Idle steps fail only on streams.
Established includes later failures; TCP connections count successful dials.

`-metrics` accepts a loopback HTTP origin for [development metrics](dev-metrics.md).
`Server.Before` is read before setup (including the page request), `Server.After`
after drain with streams still open, and `Server.Closed` after closing and a 100ms
settle. Each keeps `streams.open`, `runtime.goroutines`, `runtime.heap_inuse_bytes`,
`pool.total_conns`, `pool.max_conns`, `database.queries`,
`database.transactions_begun`, `pool.empty_acquire_count` and
`pool.empty_acquire_wait_ns` with their original JSON names. `Server.Delta` holds
`Queries`, `TransactionsBegun`, `EmptyAcquireCount` and `EmptyAcquireWaitNS`
from Before to After; negative differences indicate a counter reset.
Without `-metrics`, `Server` is null; failed metrics reads fail the command.
Metrics use the default source and a separate transport, excluded from workload
dial counts.

`-source` accepts up to 64 comma-separated loopback IPv4 literals; each dial
(page, streams, posts) binds the next source round-robin. Extra 127.x sources
require Linux or configured macOS loopback aliases. `DialFailures` counts
`TooManyOpenFiles`, `AddressUnavailableOrPortsExhausted`, `ConnectionRefused`,
`Timeout` and `Other`; harness-cancelled dials are excluded. Bound-source port
exhaustion reports EADDRINUSE on Linux, grouped with EADDRNOTAVAIL.
`Generator` reports `NOFILESoft`, `NOFILEHard`, `Goroutines` and
`HeapInuseBytes` at observation's end, before posts settle and streams close.

`-body-length` (0–4000, default 0) pads to that length without truncating the
marker; `-body-escape` pads with `&` instead of `x`. Both are recorded.
`Renders` records distinct message sequences delivered through drain: `Count`,
`SizesBytes` in sequence order, `MinBytes`, `MeanBytes`, `MaxBytes` and
`TotalBytes`. Sizes exclude SSE framing. All streams use
one channel and the default language (no language header/cookie), so sequence
identifies the [observed render](load-testing.md#what-the-runs-record-for-these).

## Reconnects and run files

`-reconnect` enables the load client's own fixed-delay model; it does not model
browser scheduling. EOF, read errors, failed dials and 503 reconnect after
`-reconnect-delay` (default 250ms, 0–10s) plus uniform random jitter from zero
through `-reconnect-jitter` (default 250ms, 0–10s), without exponential backoff.
429 and other HTTP rejections stop the stream. Only a complete event's terminating
blank line advances Last-Event-ID; until then it remains the initial cursor.
A complete `reset`, including empty data, counts and stops without reconnecting.
Every logical POST keeps its marker and body across errors and 5xx retries;
`-post-attempts` (default 3, 1–10) includes the first attempt. POST retries wait
only the fixed delay; each attempt has a 10s deadline, independently of `-reconnect`.
Without `-reconnect`, streams run once. `Reconnect` reports the model and connection
`Attempts` by outcome when enabled; `PostAttemptsMade` counts all POST attempts.
`Sent`, `Answered200`, `PostFailed` remain logical post counts.
Connection attempts count established headers, 503, ECONNREFUSED, or other;
harness cancellations are excluded. Transient failures still affect the verdict.

`-receipts PATH` exclusively creates a new file with mode 0600. The shared
run-file contract is one JSON object, defined by Go types in `cmd/loadgen/main.go`:

- `header`: required `version` (integer, currently 1), `organization_slug`,
  `channel_id` (strings), `initial_cursor`, `final_watermark` (uint64 numbers).
  All streams share the initial cursor; receipts require a valid numeric cursor.
  The final watermark is required, read from the channel page after drain until
  #628 supplies it. A failed page read fails the run rather than inventing it.
- `streams`: array in stable zero-based `index` order, one record per requested
  stream including empty ones. Each has `index`, `sequences` (object keyed by
  decimal uint64 sequence), `reset` (count), and `reconnects` (object).
- Each sequence value has `arrivals` (positive uint64 multiplicity across all
  connections) and `marker` (this run's `loadgen…Z` marker, or an empty string
  for an unmarked event). Repeated marker text in one payload is one arrival.
  Only complete non-reset events with positive sequence IDs received through drain
  are recorded. No payload, tokens or other content is stored.
- `reconnects` has required uint64 counts `established`, `503`, `refused`, `other`,
  excluding the initial connection attempt. Reset counts include all connections.
  Empty sequences are `{}`; all counts and header fields are present even if zero.

## A ceiling search

Run one step per invocation and stop at the first step that does not pass
([results](load-results.md)):

```sh
for n in 500 1000 2000 3000 5000 7000 10000; do
  go run ./cmd/loadgen -tokens /tmp/loadtest.json -metrics http://127.0.0.1:9090 \
    -streams "$n" -rate 10 -duration 30s -setup 5m -drain 60s > "step-$n.json" || break
  jq -e '.Verdict == "pass"' "step-$n.json" > /dev/null || break
done
```

Each JSON object names its verdict's reasons: `Slow` (p95 over 1 s),
`DeliveryMissing` and `RequestFailed`. The counts behind them are `Missed`,
`Sent` against `Scheduled`, `Missing`, and `Refused429`, `Refused503`,
`Reset`, `Failed` and `PostFailed`. Latencies are `P50MS`, `P95MS` and
`MaxMS`, and `Server.Delta` holds the step's database and pool costs.
