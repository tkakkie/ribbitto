# Development metrics

A development-only view of the server's counters for load tests (#218,
used by #216). **For disposable machines only.**

## Turning it on

Set `RIBBITTO_DEV_METRICS_ADDR` to a loopback IP address and a non-zero port, such as
`127.0.0.1:9090`, and `ribbitto serve` also serves `GET /metrics` there.
`make ai-env` enables metrics in the dev wrapper on `.env.local`'s metrics port.

- **Off by default.** When the setting is empty, `serve` starts no metrics
  listener and `postgres.OpenPool` installs no pgx tracer, so the server
  runs exactly as without the feature (`TestDevMetricsOffByDefault`,
  `TestOpenPoolTracer`).
- **Loopback only.** The address must be an IP literal in `127.0.0.0/8` or
  `::1` with a fixed port from 1 to 65535; port 0 is refused, since the
  kernel would pick a port the load test cannot know. Wildcards (`:9090`, `0.0.0.0`, `::`), host names such
  as `localhost` (their resolution could change) and other addresses are
  refused at start. A tunnel or proxy that forwards that port elsewhere is
  the operator's responsibility, as for databases
  ([`seed-data.md`](../seed-data.md)).
- **Its own listener.** The metrics server has its own mux and timeouts;
  the application's handler never serves `/metrics`.

## The snapshot

JSON with numbers only — never SQL text, arguments, tokens, IDs, names
or message content. Two kinds of field:

- **Counters** (`database.*`, `pool.acquire_count`, `pool.acquire_duration_ns`,
  `pool.empty_acquire_count`, `pool.empty_acquire_wait_ns`,
  `pool.canceled_acquire_count`) are cumulative since the process started
  and never reset: take a snapshot before and after each load step and use
  the difference. A restart starts them again.
- **Gauges** (`pool.idle_conns`, `pool.total_conns`, `pool.max_conns`,
  `runtime.*`, `streams.open`) are current values: read them as they are.

The `database` fields count statements **sent**, before PostgreSQL answers:
a failed statement still counts, and `transactions_committed` includes a
COMMIT that failed.

| Section | Fields |
|---|---|
| `database` | `queries` (every statement except the three below), `transactions_begun`, `transactions_committed`, `transactions_rolled_back`, counted by `postgres.QueryCounter`, a pgx tracer that looks only at a statement's first word |
| `pool` | pgx v5 `pgxpool.Stat` with its meanings: `acquire_count`, `acquire_duration_ns` (every successful acquisition), `empty_acquire_count` and `empty_acquire_wait_ns` (only acquisitions that waited for a connection), `canceled_acquire_count`, `idle_conns`, `total_conns`, `max_conns` |
| `runtime` | `goroutines`, `heap_inuse_bytes` |
| `streams` | `open`: connections in `realtime.Hub`'s registry, read through `Connections()`; `realtime` itself keeps no metrics |
