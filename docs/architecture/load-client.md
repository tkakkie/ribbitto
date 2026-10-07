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
The detailed output reference and stopping shell loop belong to #216's run.

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
