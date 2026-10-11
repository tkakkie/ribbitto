# Sidebar comparison arms and queue telemetry (#832, PR 4)

Part of #832, based on `codex/recount-writer` at d786856 (PR 3, review pending),
above PR #838 and PR #836. Only realtime's external tests and results/index
change. Production code, wiring, import edges and `realtime.SidebarInterval`
remain unchanged. PR 3 review changes require rebasing this branch.

## Arms and protocol

Use the [pool protocol](stream-sidebar-pool-results.md#reproduce) for all four
updated group commands, inherited database/caches, host gates, marker and
orchestrator load exclusion. The maintainer starts the controlled overnight run.
The 60-second window, 5/20 posts/s, 1,000/5,000 distinct streams, three rounds,
fresh fixtures, initial-count check, capped-count freshness witness, complete
message/final-snapshot drain and absolute posting accounting remain.

| Arm | Normal / recount maximum connections | Writer and admission |
|---|---|---|
| 10/20/30/40 | Shared 10/20/30/40 | Plain production synchronous loop, original sizing references |
| gate10 | Shared 10 | Production synchronous loop, #826's ten-slot source gate; reference only |
| sync40 | Shared 40 | Production synchronous loop awaits the PR 2 queue callback |
| async40 | Shared 40 | PR 3 sole writer consumes queue completions independently |
| async10+30 | Normal 10 + recount 30 | Same PR 3 writer and scheduler; only recount pool changes |

Compare sync40 with async40 to isolate writer scheduling. Both use exactly
40 workers, N registration slots for N streams, FIFO distinct-stream admission,
one running/one replaceable pending request per stream, one-slot completion
mailboxes, identical source, authorizer, caches, batch, workload and interval.
The synchronous callback waits for the same scheduler completion instead of
consuming it from the writer's idle/batch loop. Its registration cancels on
every loop exit. Plain pool40 remains separately measurable; do not silently
substitute it for the fixed-admission scheduling reference.

Compare async40 with async10+30 to isolate pool partitioning at the same total
40-connection budget. Posting, durable reads, message rendering and production
cached authorization stay in the normal pool; only independent sidebar
snapshots move to the recount pool. The worker count stays 40, including when
only 30 recount connections can be leased. Fixture/admin leases remain outside
the measured app peak and close/drop normally.

Round 1/3 order:
`10,20,10,30,10,40,sync40,async40,async10+30,async40,sync40,10,gate10`.
Round 2 order (pool candidates reversed):
`10,gate10,10,40,sync40,async40,async10+30,async40,sync40,10,30,10,20`.
Each group has 39 windows. Record spread across accepted repetitions; duplicated
sync40/async40 references bracket the partitioned candidate. Pool10 has twelve
windows; plain 20/30/40, gate10 and partitioned each three; sync40/async40 each six.
No machine load or accepted measurements are established by this handoff.

All arms call the current production `conversation.Reader.Sidebar` through the
same exported count factories and counts-only `SidebarCounter.ReadCounts`
extension: #861's SQL first-unread flags are false for recounts, while normal
page `Read` keeps them true. There is no copied pre-#861 counting SQL.
The same-snapshot `GetEventSeq` witness remains test-only and included in totals.
Snapshot bytes replace production HTML, as before. Both loops use production
`orgpg.NewCachedAuthorizer` and #859's bound-member check after render/compute
and immediately before durable/sidebar sends and heartbeats. This posting-only
fixture configures no heartbeat, identically across arms; PR 3's gated tests
cover heartbeats and revocation. Its [production gaps](stream-sidebar-writer-results.md#production-gaps),
including watches, ephemeral owners, replay/filtering and HTTP, remain explicit.

## Telemetry and eligibility

Both pools use the same tracer: SQL/transaction/count/epoch totals and
acquire-role/whole-recount attribution include both pools. A shared atomic
acquire/release counter records concurrent aggregate peak leases; independently
observed peaks are never added. Empty acquire count, cancelled acquire count
and empty-wait duration are summed across distinct pools and counted once for
shared pools. The first-empty sampler also watches the aggregate.

PR 2 mutex-protected scheduler snapshots are sampled at posting start, once a
second across posting and drain, immediately before disconnect, and after all
writers exit. Logs include offset, phase, queue length, oldest pending age,
coalesced/cancelled lifetime totals, and min/max/final for all four. Summary
arrays use `[length oldest-ns coalesced cancelled]`; each time-series row renders
age as a duration. Initial recounts precede sampling; each step has a fresh queue.
Disconnect is a separately named final phase so cancellation cannot conceal the
pre-disconnect queue. Pending successors count, and replacements retain age.
PR 3 normally coalesces invalidations in its dirty flag rather than repeatedly
submitting; zero scheduler coalesced totals are valid and do not count those
writer-level invalidations.

Latency targets remain a separate verdict. `recommendation-eligible` additionally
rejects sustained queue growth: average length **or** oldest age increases
strictly across each of three ten-second buckets in the last thirty seconds
of observed posting. Age detects a bounded queue whose length has plateaued.
Drain cannot undo that rejection. A short series or an empty bucket also
disqualifies a queue arm. This is a deterministic exclusion, not proof of
stability: inspect the entire time series for trends hidden by oscillations,
and compare posting progress/latency with the corresponding reference before
any recommendation. Final production limits/reservations, process count and
normal-work headroom remain maintainer inputs; the pool protocol's sensitivity
analysis is illustrative. No production size or design is selected.

## Functional evidence and mutation

Short functional smokes ran and passed at one and 1,000 streams for all eight
arms (plain 10/20/30/40, gate10, sync40, async40, async10+30).
They checked every scheduled post, every message/member and final count/sequence
snapshot, with zero missed/incomplete work or loop errors. Queue arms also
checked telemetry collection. Timing logs and target verdicts were suppressed;
no timing measurements, comparison or conclusion is recorded.

Reproduce each queue arm with the inherited environment described above:

```sh
export RIBBITTO_STREAM_COST=1 RIBBITTO_STREAM_COST_SMOKE=1
export RIBBITTO_STREAM_COST_SIDEBAR=1 RIBBITTO_STREAM_COST_MEMBERS=distinct
export RIBBITTO_STREAM_COST_CACHE=1 RIBBITTO_STREAM_COST_ATTRIBUTION=1
export RIBBITTO_STREAM_COST_STEPS=1,1000 RIBBITTO_STREAM_COST_RATE=5
export RIBBITTO_STREAM_COST_DURATION=2s RIBBITTO_STREAM_COST_POOL=40
for arm in sync40 async40 async10+30; do
  RIBBITTO_STREAM_COST_ARM=$arm go test -count=1 -run '^TestStreamCost$' -v -timeout 2m ./internal/realtime/
done
# Plain references: unset ARM, set POOL to 10,20,30,40 in separate invocations.
# gate10: unset ARM, POOL=10, RIBBITTO_STREAM_COST_RECOUNTS=10.
# Unset SMOKE before any controlled measurement (the ladder enforces this).
```

Deterministic telemetry uses gated scheduler work and fake time: posting-start
length, drain age, replacement/coalescing, cancellation and final values are
asserted without timing synchronization. Growth guards synthesize increasing
length and increasing age independently with latency targets already passing,
then append an empty drain; stable queues still pass.

```sh
go test -race -count=1 -run '^TestCost' -timeout 2m ./internal/realtime/
go test -race -count=1 -run '^TestCostQueueGrowthDisqualifies$' -timeout 10s ./internal/realtime/
```

The second command was run with two isolated defects, each exiting 1 at
`growing backlog accepted despite passing latency`: remove the growth condition
from `costRecommendable`; then consider only length, ignoring age. Both were
restored and the normal suite passed. No existing assertion was weakened.
Claude cross-review and tier A / Grok remain for the external orchestrator.

## Controlled results: 5 posts/s, 1,000 streams

## Controlled results: 5 posts/s, 5,000 streams

## Controlled results: 20 posts/s, 1,000 streams

## Controlled results: 20 posts/s, 5,000 streams

## Recommendation after controlled results
