# #832 sidebar recount measurements, 2026-10-12

Raw logs behind `docs/architecture/stream-sidebar-arms-measured.md`,
`stream-sidebar-arms-tables.md` and `stream-sidebar-arms-accounting.md` (PR #864).
This branch is an archive: it is never merged into `main`. `SHA256SUMS` lists
every file; check it with `shasum -a 256 -c SHA256SUMS`.

Host: darwin/arm64, 10 CPUs, Go 1.27.2, PostgreSQL 18.6 in Docker
(`max_connections` 100), loopback port 55435, one application process.
`host.log` files keep the load averages, the CPU count, container CPU and the
top processes (PID, %CPU, elapsed time and executable name only; paths and
arguments removed). Test logs are unmodified `go test -v` output.

| Directory | What | Source commit |
|---|---|---|
| `ladder/` | `TestStreamCostPoolLadder`, one invocation per group (5/1000, 5/5000, 20/1000, 20/5000): 39 windows of 60 s each | e7879c5 (PR #864 before the writer fix) |
| `rerun/` | `TestStreamCost` at 5,000 streams for sync40, async40 and async10+30, three rounds, after the writer fix | d0252bc (writer fix a68d61c) |
| `five-minute/` | `TestStreamCost` with async40 at 5,000 streams, 300 s windows, three per rate | d0252bc |

The source commits were on `codex/recount-ladder-arms`; after the squash merges
the same code is in `main` from #836, #838, #863 and #864.

## Reproduce

Each run needs `RIBBITTO_TEST_DATABASE_URL` for a disposable PostgreSQL server
(credentials are not recorded here) and `RIBBITTO_REQUIRE_DB=1`, run from the
repository root on a quiet host.

Ladder, one group per invocation:

```sh
RIBBITTO_STREAM_COST_LONG=1 RIBBITTO_STREAM_COST_LONG_GROUP=5/1000 \
  go test -count=1 -run '^TestStreamCostPoolLadder$' -v -timeout 3h ./internal/realtime/
```

Re-run and five-minute windows (`RATE` 5 or 20; `ARM` sync40, async40 or
async10+30; `DURATION` 60s or 300s). Each window waited first for a one-minute
load of at most 5:

```sh
RIBBITTO_STREAM_COST=1 RIBBITTO_STREAM_COST_SIDEBAR=1 \
RIBBITTO_STREAM_COST_MEMBERS=distinct RIBBITTO_STREAM_COST_CACHE=1 \
RIBBITTO_STREAM_COST_ATTRIBUTION=1 RIBBITTO_STREAM_COST_POOL=40 \
RIBBITTO_STREAM_COST_STEPS=5000 RIBBITTO_STREAM_COST_RATE=$RATE \
RIBBITTO_STREAM_COST_ARM=$ARM RIBBITTO_STREAM_COST_DURATION=$DURATION \
  go test -count=1 -run '^TestStreamCost$' -v -timeout 20m ./internal/realtime/
```

The re-run alternated arm order per round (`sync40 async40 async10+30 async40
sync40`, reversed in round 2); file names give rate, round, position and arm.

## From logs to tables

Per window, the `progress:`, `sidebar:` and `| <streams> | scheduled/completed/missed |`
lines give the latency percentiles, recount freshness, throughput and workload;
`queue: at=` lines are the 1 s queue samples; `attribution:` lines give SQL and
worker computation counts. The results documents state how medians, ranges and
totals were taken.
