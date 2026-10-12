# Sidebar recount arms: controlled results (#832)

Claude ran these on darwin/arm64 with 10 CPUs, Go 1.27.2 and
PostgreSQL 18.6 in Docker (`max_connections` 100), using the
[arms protocol](stream-sidebar-arms-results.md). The raw logs stayed on the
operator's machine; the [tables](stream-sidebar-arms-tables.md) and
[accounting](stream-sidebar-arms-accounting.md) carry their numbers:

- `measure832/group-{5-1000,5-5000,20-1000,20-5000}.log`: 156 ladder
  windows at e7879c5, each 60 seconds, across four rate/stream groups.
- `measure832b/w-*.log`: 30 corrected 60-second windows at d0252bc,
  after the writer fix in a68d61c; sync40/async40/async10+30 at 5,000 streams.
- `measure832c/w-*.log`: six five-minute async40 windows at d0252bc,
  three per rate, at 5,000 streams. Each has `host.log`.

The pre-window gate required one-minute load at most half the CPU count.
The ladder host log contains intermittent load, process and container snapshots;
the later host logs record pre-window loads without process snapshots.
That no other heavy work ran is an **operator observation**, not a conclusion
proved by these logs. Neither the gate nor
snapshots establish continuous host
quiet. These are single-host, single-process synthetic results, not a capacity
ceiling or production-wiring validation.

## Metrics and aggregation

[Latency/workload tables](stream-sidebar-arms-tables.md) retain every arm and
group separately for the ladder, corrected re-run and five-minute run.
[Accounting tables](stream-sidebar-arms-accounting.md) contain SQL, transaction,
count, sent recount, empty/cancelled acquisition and summed wait totals, peak
leases, elapsed time and sent recount throughput.

`costTargets` in `internal/realtime/cost_long_test.go` tests **post-return-to-send
p95 ≤ 250 ms**, recount freshness p95 ≤ 2 s / p99 ≤ 5 s, all scheduled posts
completed, no misses, missing messages, final-snapshot timeout or loop errors,
and nonzero deliveries. `internal/realtime/cost_test.go` computes message latency
as send time minus `Post` return time, allowing negative samples. Posting latency
is `Post` entry to return. **Scheduled-to-delivery** starts at the scheduled post
time and includes dispatch lateness and posting; it is a separate clock.
Freshness starts at post return and ends at the first sent sidebar snapshot
covering both count and committed sequence for that member.

For example, pool10 at 5 posts/s / 1,000 streams has median post-return-to-send
p95 **148.34 ms**, versus scheduled-to-delivery p95 **355.72 ms**; its 12 target
passes are consistent with the former. The historical one-second verdict and
successful test exit are separate from target passes. Eligibility additionally
requires the queue telemetry and growth guard specified by the arms protocol.

For each group/arm, sort logged per-window percentiles or scalar metrics;
report the middle value (mean of the middle two for even counts) and the
minimum–maximum, rounding after aggregation. Counts and cumulative wait durations
are summed, not medians. No failed window is dropped; no combined percentile is
inferred from per-window percentiles. Posting-only queue bucket means average
samples equally, not by time; drain/disconnect samples are excluded. Worker
computations below use attribution's `whole-sidebar samples`, while sent
recounts use the sidebar line. All measurement totals exclude initial setup and
include posting plus drain.

## Workload completion

The ladder scheduled/completed/missed totals are **117,000/115,809/1,191**.
**25 windows missed posts.**
All misses are at 5,000 streams. The table identifies every such window in
`group-<rate>-5000.log`; entries are `run: missed`, and completed is scheduled
minus missed. Scheduled per window is 300 at 5 posts/s and 1,200 at 20 posts/s.

| Rate | Round | Arm | Run: missed |
|---|---|---|---|
| 5 | 1 | 10 | 1:16, 3:18, 5:16, 12:32 |
| 5 | 2 | 10 | 1:16, 3:4, 10:10, 12:8 |
| 5 | 3 | 10 | 1:15, 5:24, 12:17 |
| 5 | 3 | 20 | 2:10 |
| 20 | 1 | 10 | 3:90, 5:25, 12:75 |
| 20 | 1 | 30 | 4:2 |
| 20 | 2 | 10 | 1:16, 3:52, 10:91 |
| 20 | 3 | 10 | 1:232, 3:12, 5:131, 12:252 |
| 20 | 3 | 20 | 2:22 |
| 20 | 3 | 30 | 4:5 |

Every other ladder window completed every post. Across all repetitions, every
arm at 1,000 streams completed; at 5,000 streams, pool30/40, gate10, sync40 and
both async arms completed at 5 posts/s, and pool40, gate10, sync40 and both async
arms completed at 20 posts/s. Pool10/20 at either rate and pool30 at 20 posts/s
have the misses listed above. Every re-run and five-minute window completed,
each set totaling **22,500/22,500/0** posts. All logged loop-error
counts across the three sets are zero. Completion alone does not imply target
success.

## Writer starvation found by the ladder

At 5,000 streams before the fix, workers computed many recounts but writers
sent few. Ranges below are per-window min–max; oldest age is the maximum
**sampled oldest pending age** across the group's queue snapshots, not a
per-completion queue-wait latency.

| Rate | Arm | Computed/window | Sent/window | Sent/stream/s | Oldest age maximum ms | Freshness p95 range s |
|---|---|---|---|---|---|---|
| 5 | async40 | 245580–248024 | 6784–10635 | .022–.035 | 309.42 | 54.99–57.27 |
| 5 | async10+30 | 224030–225366 | 5675–5686 | .018–.018 | 488.27 | 57.91–57.93 |
| 20 | async40 | 205364–206812 | 5120–5182 | .017–.017 | 671.64 | 57.93–58.10 |
| 20 | async10+30 | 184038–191393 | 5120–5125 | .017–.017 | 1051.21 | 58.10–58.30 |

The writer implementation explains a mechanism consistent with this evidence:
requiring equality with the latest desired version discards a completion when
later posts invalidate it before consumption. The fix sends a result newer than
the last sent version and keeps the stream dirty for later invalidations. The
[deterministic writer tests](stream-sidebar-writer-results.md) establish that
progress rule. Aggregate freshness percentiles and sampled pending age do not
locate every frame in time or prove sends occurred only after posting stopped.

Corrected sent recount throughput ranges are **.797–.800 / .720–.725** at
5 posts/s and **.657–.672 / .605–.611** at 20 posts/s, shared/partitioned
respectively. Both async arms pass all latency/workload targets in the re-run;
eligibility is shared **3/6**, partitioned **0/3** at 5 posts/s and shared **4/6**,
partitioned **3/3** at 20 posts/s. The 1,000-stream async ladder results still
predate the fix; they passed but were not re-run.

## Five-minute queue evidence

Across posting-only minute means for **minutes 2–5** of all three repetitions:

| Rate | Mean pending length range | Mean oldest pending age range ms |
|---|---|---|
| 5 | 919–971 | 228–244 |
| 20 | 1593–1663 | 480–514 |

These are rounded ranges of twelve minute means per rate. Minute 1 is excluded:
its ranges are 904–919 / 223–228 ms at 5 posts/s and 1528–1582 / 450–474 ms
at 20 posts/s. Later minute means fluctuate without a consistent minute-by-minute
climb. That supports an apparent plateau at this
resolution over the observed horizon, not established long-run stability.

All six windows pass the targets. Median post-return-to-send p95 is
**25.25 / 25.84 ms**, separate from scheduled-to-delivery p95 **35.16 / 35.61 ms**
at 5/20 posts/s. Median freshness p95/p99 is **1224.77/1293.05 ms** at 5 posts/s
and **1476.25/1576.19 ms** at 20; full spreads are in the latency tables.

The guard rejects `w-5-2.log` and `w-5-3.log`, accepting `w-5-1.log` and all
three `w-20-*.log` windows. It tests three consecutive ten-second means within
`[last posting sample − 30 s, last posting sample)`, excluding the endpoint.
Here that is approximately seconds 269–299, with actual nanosecond offsets
controlling bucket membership; buckets can contain different sample counts.

| Window | Bucket sample counts | Mean lengths, oldest age ms (three buckets) | First-to-third rise |
|---|---|---|---|
| w-5-2 | 9/10/10 | 881 → 991 → 1105.4; 218.68 → 249.83 → 283.61 | length 25.5%, age 29.7% |
| w-5-3 | 10/10/9 | 888.8 → 902.1 → 891.89; 214.95 → 219.33 → 220.87 | age 2.8% |

Both rejections are valid under the specified strict-growth rule: w-5-2 rises
in both metrics; w-5-3 rises in age despite falling final length. The larger
rise cannot be dismissed as noise. Minute averages can hide that shorter trend;
the logs do not resolve whether it is transient or persists beyond the window.
The guard's rejection remains a bar to recommendation, even with target passes.
Any revised stability criterion must be decided in advance of validation, with
an explicit horizon and acceptable growth; these results cannot retrospectively
waive the guard.

## What the comparisons support

At 1,000 streams every configuration passes the targets. At 5 posts/s, rounded
plain-pool arm medians for posting p99 span **135–301 ms**, versus **10–13 ms**
for gate10 and queue arms; at 20 posts/s, **69–242 ms** versus **7–11 ms**.
At 5,000 streams plain pools fail the targets; pool10 posting p99 at 5 posts/s
spans **909.04–1732.83 ms**, illustrating why spread and misses matter.
Gate10/sync40 keep posting fast but hold message delivery behind recounts.
With the same shared40 pool and queue, corrected async40 passes every target;
sync40 fails at both rates. This isolates writer scheduling in the prototype.

Partitioning improves posting and message latency but reduces sent recount
throughput and worsens freshness. Corrected per-window ranges at 5,000 streams:

| Rate / arm | Posting p99 ms | Post-return-to-send p95 ms | Scheduled-to-delivery p95 ms | Freshness p95 ms | Sent/stream/s |
|---|---|---|---|---|---|
| 5 / shared40 | 15.86–19.45 | 26.00–28.15 | 36.53–40.42 | 1214.50–1222.78 | .797–.800 |
| 5 / normal10+recount30 | 13.60–14.62 | 21.59–22.85 | 30.36–31.07 | 1326.23–1335.64 | .720–.725 |
| 20 / shared40 | 14.58–17.14 | 25.60–26.28 | 35.52–36.21 | 1436.43–1478.23 | .657–.672 |
| 20 / normal10+recount30 | 10.75–12.08 | 21.23–21.73 | 28.57–29.16 | 1581.93–1594.22 | .605–.611 |

The ranges separate at both rates. These measurements establish a tradeoff,
not a universally preferable pool arrangement.

## Recommendation and open decisions

A **single writer per stream with a bounded, fair, cancellable recount queue**
is a promising candidate. Shared40 is a useful prototype reference; pool layout
and production size remain open. Preserve newer-than-last-sent completion checks,
dirty state for later invalidations and bound-member authorization before sends.
Production recommendation remains pending a stability criterion decided in
advance and validation with [production wiring](stream-sidebar-writer-results.md#production-gaps),
including the real wait, normal-work headroom and representative workloads.
Completed controlled measurements do not close those decisions or the observed
growth-guard failures.
