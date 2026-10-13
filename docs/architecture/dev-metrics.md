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

JSON with numbers, numeric arrays and state booleans only — never SQL text,
arguments, tokens, IDs, names or message content. Two kinds of numeric field:

- **Counters** (`database.*`, `pool.acquire_count`, `pool.acquire_duration_ns`,
  `pool.empty_acquire_count`, `pool.empty_acquire_wait_ns`,
  `pool.canceled_acquire_count`) are cumulative since the process started
  and never reset: take a snapshot before and after each load step and use
  the difference. A restart starts them again.
- **Gauges** (`pool.acquired_conns`, `pool.idle_conns`, `pool.total_conns`, `pool.max_conns`,
  `runtime.*`, `streams.open`) are current values: read them as they are.

The `database` fields count statements **sent**, before PostgreSQL answers:
a failed statement still counts, and `transactions_committed` includes a
COMMIT that failed.

| Section | Fields |
|---|---|
| `database` | `queries` (every statement except the three below), `transactions_begun`, `transactions_committed`, `transactions_rolled_back`, counted by `postgres.QueryCounter`, a pgx tracer that looks only at a statement's first word |
| `pool` | pgx v5 `pgxpool.Stat` with its meanings: `acquire_count`, `acquire_duration_ns` (every successful acquisition), `empty_acquire_count` and `empty_acquire_wait_ns` (only acquisitions that waited for a connection), `canceled_acquire_count`, `acquired_conns` (currently held), `idle_conns`, `total_conns`, `max_conns` |
| `runtime` | `goroutines`, `heap_inuse_bytes` |
| `streams` | `open`: connections in `realtime.Hub`'s registry, read through `Connections()` |

## Scheduler snapshot

`scheduler` exposes every [RecountStats field](recount-scheduler.md), sampled
together under its mutex. Other sections are sampled independently.
The metrics dependency is optional: nil yields `enabled: false` and every other
value zero (including arrays). This is the production default until part 2h of
#865 wires a scheduler. Scraping never constructs or activates one.

With a scheduler, `enabled: true`; `closed` reports stopped admission, and
`snapshot_at_unix_ns` timestamps the gauges. `worker_limit` and
`registration_limit` are configured bounds. `pending` includes successors behind
running work; `eligible` counts queued registrations. `running_workers`,
`waiting_workers`, `registrations`, `oldest_pending_age_ns` and
`oldest_running_age_ns` describe current occupancy. Idle has no pending/running
work; worker saturation has `running_workers == worker_limit`, with `eligible`
showing queued demand. Registration saturation has `registrations ==
registration_limit`. Cancelled running work retains capacity until actual return.

Lifetime counters are `completed`, `processing_duration_ns`,
`processing_duration_bucket_counts`, `cancelled`, `refused_capacity`,
`refused_closed`, `rejected_submissions`, `errors` and `errors_after_cancellation`.
The six bucket counts are disjoint, summing to `completed`. The first five have
inclusive upper bounds in `processing_duration_bucket_upper_bounds_ns`
(1ms, 10ms, 100ms, 1s, 10s); the sixth is over 10s. Each bucket excludes durations
covered by earlier buckets. Processing duration measures dispatch to actual
return, including cancelled work; it does not measure delivery or freshness.

`errors` counts returned errors and recovered panics from live registrations;
`errors_after_cancellation` counts those whose registration was already cancelled
when the worker processed the return under the scheduler mutex. They are disjoint
and classify by registration state, regardless of the error value. A cancellation
that lands after return processing counts as a live error. Thus `errors` measures
live recount failures separately from cancellations and refusals.
`cancelled` counts removed pending work and first cancellation of running work,
even if it later succeeds; it can overlap `errors_after_cancellation` and must not
be subtracted from either error counter. All counters persist after closure.

## Delivery snapshot

Part 2f adds optional `delivery`, sampled under its own observer mutex. Nil gives
`enabled: false` and zero values, independently of `scheduler`; production supplies
neither until 2h. `submitted` counts scheduled writer attempts (including refused
submissions), `received` counts actual mailbox receives, and `delivered` counts
successful Sender returns whose context is still live. This does not acknowledge
browser receipt or DOM application. `denied` counts membership revocation;
`failed` counts computation/panic, authorization-check, submission and send errors.
`cancelled` counts outstanding attempts abandoned on writer exit or cancellation,
including buffered/late results. These terminal outcomes are disjoint; cancellation
during authorization/send takes precedence over failure. Ordinary exit after a
successful delivery adds no cancellation. Counters persist for the observer's life.

`queue`, `compute`, `completion_to_send` and `submit_to_send` each expose `count`,
`total_ns` and six disjoint `bucket_counts`, using `bucket_upper_bounds_ns` with
[the scheduler bounds](#scheduler-snapshot). Queue samples end at computation
invocation; compute includes watch capture and actual return, even after writer
exit. Completion-to-send includes mailbox wait, writer work before delivery,
authorization and Sender time. Both send durations sample only delivered attempts;
denied, failed and cancelled attempts contribute nothing. Populations can differ
between snapshots: queue starts, compute returns and sends are separate stages.

Stage 2 can separate waiting for a worker, computation cost and delay after return,
plus total submission-to-send latency. It cannot infer freshness relative to
persisted changes, count coalesced invalidations, or prove which committed changes
a frame covers. PR 3 must supply a same-snapshot witness or equally rigorous oracle.
Only aggregates survive requests: no IDs, payloads or request history are retained.
