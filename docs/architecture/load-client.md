# Development load client

With [seed credentials](../seed-data.md) on a disposable
machine and `make dev` running, use:
```sh
go run ./cmd/loadgen -tokens /tmp/loadtest.json -streams 8 -duration 5s -rate 2
```
For loopback Caddy, add `-target https://localhost:8443 -ca /path/to/root.crt`.
Only that PEM pool is trusted; HTTP uses HTTP/1.1, HTTPS negotiates HTTP/2.
The dialer checks every resolved address; proxies and origin changes are refused.
Limits: 10 minutes, 100,000 stream attempts, 100 posts/s (serial, no catch-up).
Accounts and their sessions rotate; all traffic uses the first fixture channel.
`-cursor` supplies Last-Event-ID (default 0); an empty value exercises HTTP 400.
Established counts include streams later reset/failed. At expiry, posting stops,
the last POST finishes, then streams close; none of that counts as a failure.
TCP connections count successful dials, separately from stream attempts.
Only aggregate counts are printed. No reconnects, credential output or reports.
