# Sidebar stage-1 plan (#832, stability work part 3)

Pre-registered on 2026-10-12, before measurement. Claude runs this on a quiet
host after all three stability parts land. No stage-1 measurement ran here.
The [historical arms](stream-sidebar-arms-results.md) retain their meanings;
their earlier 40-worker results do not validate these configurations.

## Budget and configurations

Assume M=100 PostgreSQL maximum connections, R=3 reserved, E=20 other clients
and operations, P=2 peak processes (including update overlap), and H=10 normal
work headroom per process. The ceiling is floor((M−R−E)/P)=38 per process;
both configurations use 36, leaving two spare. E and H are not double-counted:
E is outside the process pools; H is inside. These are assumptions, **not
production settings**; re-check them for the deployment. P=2 does not establish
multi-process delivery support.

| Configuration | Normal pool | Recount pool | Workers | Total connections |
|---|---|---|---|---|
| shared | 36 | same pool | 26 | 36 |
| partitioned | 10 | 26 | 26 | 36 |

Shared H is a logical bound, not a dedicated reservation. Partitioned isolates
only sidebar snapshots; posting, durable reads, rendering reads and cached
authorization use the normal pool. Both use the test-only async single writer
and fair scheduler, N registration slots for N streams, distinct members,
shared read/message caches, batch 100, no stagger/gate and identical fixtures.
Fresh migrated databases and initial recounts precede each posting window.
Fixture/admin leases are outside measured application leases; record actual
server limits and other clients. The harness logs workers, both pool maxima
and whether the pool is shared in every window.

## Fixed criterion and targets

Use the [stability criterion](stream-sidebar-stability.md#new-window-rule-and-telemetry):
three independent 600 s posting windows, 60 s warm-up excluded from trends,
adjacent two-minute means sliding by 60 s and minutes 2–4 versus 8–10.
Length rise tolerance is max(5% of the earlier mean, 0.5% of registration bound);
oldest-age tolerance is max(5%, 10 ms). Either exceeded rejects. Short spikes
are logged for cause analysis; there is no terminal-30/60-second veto.

Retain telemetry completeness, every scheduled post completed, no missed posts,
missing deliveries/final snapshots, timeouts or loop errors, delivery p95 ≤250 ms,
and recount p95 ≤2 s and p99 ≤5 s (`costTargets`). Compare posting latency and
scheduled-to-delivery p50/p95/p99 between configurations at the same load too;
report median and min–max across repetitions, alongside queue growth/spikes,
SQL/recount/pool-wait totals and aggregate peak leases. Test exit success alone
does not establish target success.

## Matrix and commands

1,000/5,000 streams × 5/20 posts/s × shared/partitioned × three repetitions
= **24 windows**, each posting for 600 s. Round 1/3 runs shared then partitioned;
round 2 reverses them within each rate/streams group. Each group invocation runs
six windows and emits two `stage1 group=... windows=.../3 passed=.../3
recommendation-eligible=...` summaries. Exactly three windows must each pass
`costTargets` and stability; missing, incomplete, rejected or inconclusive
windows make that configuration-group ineligible. Summaries include unrun groups
after an ordinary failure; a killed/timed-out process cannot certify a group.

Export the disposable local test database URL ([database tests](../database-tests.md))
in the inherited environment first. Archive each command's complete output and
exit status separately; do not run them concurrently or with the race detector.

```sh
export GOCACHE=$PWD/bin/.cache/go-build GOMODCACHE=$PWD/bin/.cache/mod
export GOLANGCI_LINT_CACHE=$PWD/bin/.cache/lint GOPATH=$PWD/bin/.cache/gopath
export RIBBITTO_REQUIRE_DB=1 RIBBITTO_STREAM_COST_STAGE1=1
RIBBITTO_STREAM_COST_LONG_GROUP=5/1000 go test -count=1 -v -run '^TestStreamCostStage1$' -timeout 3h ./internal/realtime
RIBBITTO_STREAM_COST_LONG_GROUP=5/5000 go test -count=1 -v -run '^TestStreamCostStage1$' -timeout 3h ./internal/realtime
RIBBITTO_STREAM_COST_LONG_GROUP=20/1000 go test -count=1 -v -run '^TestStreamCostStage1$' -timeout 3h ./internal/realtime
RIBBITTO_STREAM_COST_LONG_GROUP=20/5000 go test -count=1 -v -run '^TestStreamCostStage1$' -timeout 3h ./internal/realtime
```

Unset/empty group selects all 24 (`-timeout 12h`). The runner owns the root
MEASURING marker exclusively and removes it on normal exit. Existing
[host gates](stream-sidebar-pool-results.md#reproduce) apply: required load/CPU
probes before/after every window, wait/retry every 10 s before starting when
one-minute load exceeds half the CPUs, post-load logged only. The orchestrator
holds other heavy jobs while the marker exists, records host/process conditions
and excludes interference; averages alone cannot establish a quiet host.

Estimated wall time is about **4 h 5–10 min plus load waits**. Local archive
`e76cb4c922306fe938daa61b3da63ebab22eebdb`, `ladder/group-*.log`, has 78
setup/drain pairs per stream count: median setup/drain 3.063/0.838 s at 1,000,
14.645/1.948 s at 5,000 (setup ranges 2.912–3.327/14.386–15.774 s;
drain 0.169–1.400/1.057–5.290 s). Adding those medians for twelve windows each
to 14,400 posting seconds gives 4 h 4 min, before cleanup/host waits. These are
legacy costs, not new measurements; timeout allowances are not estimates.

## Disposition and functional evidence

Viable: a configuration meets every group summary under controlled conditions,
with acceptable posting/scheduled-delivery comparisons. Not viable within this
budget: controlled complete evidence fails a retained target or stability.
Needs work: incomplete/inconclusive windows, interference or unexplained spikes
or latency regressions; investigate and repeat the affected three-window group
without changing the fixed criterion. If neither meets the targets within this budget,
propose the connection limit or configuration needed, not just more workers.

Short functional smokes of shared and partitioned at **1 and 1,000 streams**
passed: all posts/messages/final snapshots and queue telemetry checked. No smoke
timings are recorded. Reproduce with `TestStreamCost`, `RIBBITTO_STREAM_COST=1`,
`RIBBITTO_STREAM_COST_SMOKE=1`, arm `async`, workers `26`, steps `1,1000`, rate
`20`, duration `6s`, members `distinct`, cache/sidebar/attribution `1`; set pool
`36` with recount pool unset for shared, or pool `10` and recount pool `26` for
partitioned (all keys use the `RIBBITTO_STREAM_COST_` prefix).

Stage 2 mixed load, its normal-work targets/refusal rates/connection allowances,
is defined separately before it runs. Stage-1 viability allows #865 parts 1b–1f
to resume; production wiring and stage 2 precede activation. Nothing here
authorises production activation.
