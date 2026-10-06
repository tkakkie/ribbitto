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
