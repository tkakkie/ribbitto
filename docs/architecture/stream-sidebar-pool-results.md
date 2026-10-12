# Sidebar pool comparison (#832, part 1)

Part 1 extends #826 and proves a synchronous recount stops the same stream's
writer. It selects no pool size and proves no writer independence. The
[PR 2 scheduler](stream-sidebar-queue-results.md) covers queue safety only;
[PR 3 writer](stream-sidebar-writer-results.md) and
[PR 4 arms/telemetry](stream-sidebar-arms-results.md) now complete the test-local comparison setup.

## Evidence and measurement status

Verified 2026-10-11 on darwin/arm64, Darwin 27.0.0, Go 1.27.2, 10 CPUs.
The [#826 hardware record](stream-sidebar-bottleneck-results.md) was not
independently verified here. PostgreSQL 18.6, max_connections=100,
superuser_reserved_connections=3, reserved_connections=0; loopback port 55435,
one app process.

Required `sysctl -n vm.loadavg` and `sysctl -n hw.ncpu` probes failed with
`Operation not permitted` in the earlier sandbox run; the ladder refused to begin. There are **no accepted 60-second results, repetitions or
spread**, and no 1,000/5,000-stream target verdicts in this part. Do not substitute
#826's uncontrolled four-second runs or the smoke check below. Absence of other
heavy work cannot be established here; `ps` was not used.

The earlier functional smoke (one distinct-member stream, pool10, cache and
attribution, 20 posts/s for 6 s) completed 120/120 posts, zero missed, all messages
and the final snapshot beyond the cap. SQL/transaction totals: 1,384/742; counts
24; recounts six; posting-plus-drain 6.074 s. It preceded the peak-counter
refinement and validates function, **not** performance targets or pool sizing.

Channel-gated tests prove a synchronous recount stops the same stream's writer;
on release, an id-free sidebar frame precedes message ID 1. Splitting the pool
would still stop this writer. Freshness tests reject stale sequence 1,199 for
1,200 messages with capped totals 100 and reject a missing message. A PostgreSQL
test pins counts and witness before a later commit, then proves a new snapshot
sees it. Group selection and pre-only load gating also have deterministic,
mutation-checked tests (details in the local PR draft).

## Reproduce

Export `RIBBITTO_TEST_DATABASE_URL` as `<local test database>` first
([database tests](../database-tests.md)); credentials belong only in the inherited
environment. Run one group per invocation so a timeout cannot lose the whole
matrix. The orchestrator runs these outside the sandbox, using port 55435:

```sh
export GOCACHE=$PWD/bin/.cache/go-build GOMODCACHE=$PWD/bin/.cache/mod
export GOLANGCI_LINT_CACHE=$PWD/bin/.cache/lint GOPATH=$PWD/bin/.cache/gopath
export RIBBITTO_REQUIRE_DB=1 RIBBITTO_STREAM_COST_LONG=1
RIBBITTO_STREAM_COST_LONG_GROUP=5/1000 go test -count=1 -run '^TestStreamCostPoolLadder$' -v -timeout 3h ./internal/realtime/
RIBBITTO_STREAM_COST_LONG_GROUP=5/5000 go test -count=1 -run '^TestStreamCostPoolLadder$' -v -timeout 3h ./internal/realtime/
RIBBITTO_STREAM_COST_LONG_GROUP=20/1000 go test -count=1 -run '^TestStreamCostPoolLadder$' -v -timeout 3h ./internal/realtime/
RIBBITTO_STREAM_COST_LONG_GROUP=20/5000 go test -count=1 -run '^TestStreamCostPoolLadder$' -v -timeout 3h ./internal/realtime/
# Optional all-groups invocation; an empty selector runs all four groups.
RIBBITTO_STREAM_COST_LONG_GROUP= go test -count=1 -run '^TestStreamCostPoolLadder$' -v -timeout 12h ./internal/realtime/
```

The opt-in test fixes 60 s, rates 5/20, streams 1,000/5,000 and three rounds.
PR 4 retains plain synchronous pools 10/20/30/40 and pool10/gate10, adding
queue-backed sync40, async40 and async10+30. Each group now has 39 windows:
13 per round, with pool10 interleaved and reversed order in round 2.
The [arms protocol](stream-sidebar-arms-results.md) specifies exact order,
fixed scheduler parameters and telemetry. Three hours per group and twelve
for the optional 156-window matrix are timeout allowances, not run-time claims;
load waits remain unbounded. Invalid selectors fail before marker creation.

Every window uses a new migrated disposable database, one organisation,
one channel/default topic, distinct members, unfragmented ranges and alternating
initial totals 0/1, shared event/message caches, authorization capacity 10,000,
batch 100 and no stagger. Plain sizing varies only pool size; the gate and
queue comparisons have the explicitly isolated variables in the arms protocol.
Fixtures and initial recounts are excluded from measured metrics. HTTP/HTML,
network/slow clients, presence/typing, moves/reads, fragmented ranges, large
sidebars and deep history are absent.

Before/after every window, the driver logs UTC time and both required host
probes. A pre-run one-minute load above half the CPU count waits ten seconds and
retries the probe, allowing the previous window's own load to decay. Post-run
load is logged only: it includes this workload and does not reject or retry a
window. A failed/unavailable probe stops the ladder. The test gates on load
before each window; absence of other heavy work is checked by the orchestrator,
not by the test. The orchestrator holds every other heavy job while MEASURING
exists, records host conditions including a process list, and disqualifies runs
with known interference. Bracketing load averages cannot prove its absence.

The driver creates an empty MEASURING marker at the worktree root, refuses an
existing marker and removes its own marker on exit. Its relative path assumes
Go runs the package from internal/realtime. Logs include ladder invocation wall
time (host waits included), each window's invocation wall time (cleanup included),
setup through initial recounts, configured posting window, posting-plus-drain and
drain beyond the window. Setup/drain costs can therefore guide later timeouts.

The deterministic tests and the smoke check above can be reproduced with:

```sh
go test -race -count=1 -run '^TestCost' -v ./internal/realtime/
RIBBITTO_STREAM_COST=1 RIBBITTO_STREAM_COST_MEMBERS=distinct \
  RIBBITTO_STREAM_COST_CACHE=1 RIBBITTO_STREAM_COST_SIDEBAR=1 \
  RIBBITTO_STREAM_COST_ATTRIBUTION=1 RIBBITTO_STREAM_COST_POOL=10 \
  RIBBITTO_STREAM_COST_STEPS=1 RIBBITTO_STREAM_COST_RATE=20 \
  RIBBITTO_STREAM_COST_DURATION=6s go test -count=1 -run '^TestStreamCost$' \
  -v -timeout 2m ./internal/realtime/
```

## Metrics and comparability

The test-only count adapter reads org's committed event sequence through its
exported snapshot-bound cursor after the four count statements. PR 4 uses production
counts-only recounts (#861), disabling first-unread probes. It adds exactly
one `GetEventSeq` statement per recount, classified separately by attribution
and included in SQL and whole-sidebar elapsed totals. The witness and both
counts share the lists' read-only repeatable-read transaction. Production would
not need this measurement-only query. New pool baselines must use this witness
as well; the old #826 numbers have less SQL and shorter/different histories.

Each connection sends the two totals and witness in place of HTML. Completion
requires every posted message plus a final snapshot whose totals cover
min(initial + completed posts, 100) and whose witness covers the last committed
post. Recount delay uses the first sent snapshot covering each post by both
count and sequence, per member, even beyond the cap. Older snapshots cannot
satisfy a later post merely because both badges show 99+.

Open-loop scheduling, one second of in-flight posts, 30 s drain, complete-message
checks, missed/incomplete accounting and attribution remain.
Posting latency is entry to `Post` until return, per successful post.
Scheduled-to-delivery includes dispatch lateness and posting, per message/member;
message delivery retains #826's return-to-send clock (negative samples allowed).
The new progress line logs their p50/p95/p99, successful acquire/release peak
leases in the measured app pool and stream-loop errors. The 50 ms sampler still
records the first empty acquisition. Pool wait is summed empty-acquire time,
not wall time; fixture/admin connections are outside the peak counter.

A separate provisional verdict requires all scheduled posts completed, zero
misses, no missing messages/final-snapshot timeout or loop errors, message p95
<=250 ms, recount p95 <=2 s and p99 <=5 s. The historical 1 s ladder verdict is
still logged independently. Test exit success alone is not target success.
Posting must be compared with the same-load reference for each comparison
before recommending anything; target success does not establish a production ceiling.

Report controlled SQL/count/recount/wait totals, peak leases,
scheduled/completed/missed and latency percentiles, with median and min–max
across accepted repetitions. PR 4 now samples queue length, oldest waiting age,
coalesced/cancelled work over time; growing backlog disqualifies a recommendation
regardless of latency. Part 1 has no new application queue.

## Connection budget sensitivity

Production limits, reservations, process count and headroom are unknown. This
**illustration** assumes three superuser-reserved connections, no additional
reservations and 20 installation-wide connections for normal/operational work.
Measured posting/message work shares the app pool with recounts. Replace these
assumptions with actual traffic and clients before choosing a configuration.

For pool K and P processes, demand P*K + 20 must be <= max_connections - 3.
The table gives per-process budgets and fitting tested sizes; it proves no
multi-process delivery (out of scope).

| max_connections | 1 process budget / fitting sizes | 2 processes | 4 processes |
|---|---|---|---|
| 100 | 77 / 10,20,30,40 | 38 / 10,20,30 | 19 / 10 |
| 200 | 177 / 10,20,30,40 | 88 / 10,20,30,40 | 44 / 10,20,30,40 |
| 500 | 477 / 10,20,30,40 | 238 / 10,20,30,40 | 119 / 10,20,30,40 |

With R additional reserved connections subtract R before dividing by P.
An asynchronous queue using the shared pool costs the same connection budget
as pool K, but must bound admitted/active work separately. Separate normal N and
recount B pools require P*(N+B)+20; a fair queue alone adds no connections.
For example N=10/B=30 has the same total 40 as pool40, so compare them at equal
total budgets in PR 4. Splitting a synchronous source's pool is neither writer
independence nor fewer connections. Do not adopt pool30 from historical results.

## Recommendation and remaining work

The [controlled results](stream-sidebar-arms-measured.md) recommend a single
writer per stream with recounts on a bounded queue over the shared pool; plain
pool sizing alone fails at 5,000 streams. The [scheduler](stream-sidebar-queue-results.md),
[single writer](stream-sidebar-writer-results.md) and
[comparison arms](stream-sidebar-arms-results.md) remain test-only evidence.
