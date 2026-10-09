# Development metrics

A development-only view of the server's counters and CPU, mutex and heap
profiles for load tests (#218, #695, used by #216). **For disposable machines only.**

## Turning it on

Set `RIBBITTO_DEV_METRICS_ADDR` to a loopback IP address and a non-zero port, such as
`127.0.0.1:9090`, and `ribbitto serve` also serves `GET /metrics` and the
three profile endpoints below there.
`make ai-env` enables metrics in the dev wrapper on `.env.local`'s metrics port.

- **Off by default.** When the setting is empty, `serve` starts no metrics
  listener, `postgres.OpenPool` installs no pgx tracer, and mutex sampling
  is unchanged (`TestDevMetricsOffByDefault`, `TestOpenPoolTracer`).
- **Loopback only.** The address must be an IP literal in `127.0.0.0/8` or
  `::1` with a fixed port from 1 to 65535; port 0 is refused, since the
  kernel would pick a port the load test cannot know. Wildcards (`:9090`, `0.0.0.0`, `::`), host names such
  as `localhost` (their resolution could change) and other addresses are
  refused at start. A tunnel or proxy that forwards that port elsewhere is
  the operator's responsibility, as for databases
  ([`seed-data.md`](../seed-data.md)).
- **Its own listener.** The metrics server has its own mux and timeouts;
  the application's handler never serves `/metrics` or `/debug/pprof/` paths.
  Importing `net/http/pprof` registers handlers on `http.DefaultServeMux`
  even with a named import. Neither server serves that mux: only the three
  profiles are registered by hand on the metrics mux. The index, goroutine,
  block, trace, command-line and symbol endpoints are absent.

## Capturing profiles

From the disposable machine while the server is under load:

```sh
go tool pprof 'http://127.0.0.1:9090/debug/pprof/profile?seconds=30'
go tool pprof 'http://127.0.0.1:9090/debug/pprof/mutex?seconds=30'
go tool pprof 'http://127.0.0.1:9090/debug/pprof/heap'
```

CPU samples show where execution spends time; mutex samples show contention
time attributed to the stacks releasing contended locks. Mutex sampling is
enabled only with metrics, at fraction **5** (on average one in five contention
events). It is process-wide and stays enabled until process exit; restart
without the setting to turn it off. The mutex command above captures a delta
over 30 seconds; without `seconds`, it returns cumulative samples. Heap samples
show allocation stacks and live memory; `?gc=1` first forces garbage collection.

Profiles reveal function names, source paths and stacks, never message content.
Keep both listener and captures on the disposable machine, with the same
loopback-only restriction as the counters. CPU profiling permits one capture
at a time. CPU defaults to 30 seconds; positive `seconds` selects the duration.
For timed profiles, Go's handlers extend the metrics server's 10-second write
deadline by that duration, so a 30-second capture gets 40 seconds. There is no
additional capture-duration cap; shutdown still has a 10-second grace period
and can interrupt a capture. See [load results](load-results.md#reproducing-it)
for the 15,000-stream HTTP/1.1 step.

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
