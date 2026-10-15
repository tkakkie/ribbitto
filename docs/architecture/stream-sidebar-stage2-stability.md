# Stage-2 stability evaluator (#865, PR 3 slice 5c)

`cmd/loadgen/stage2_stability.go` reimplements the unchanged [#867 rule](sidebar-counts-history.md#stability-and-validation-plans)
and replaces the async stability placeholder; sync remains inconclusive.
Stability rejects incomplete full-window evidence because #867 fixes that outcome, whereas the pinned headroom protocol makes incomplete evidence inconclusive.
Short windows are inconclusive, checked before completeness or bound validity.

Evidence sources preserve the [5a](stream-sidebar-stage2-telemetry.md) and [5b](stream-sidebar-stage2-headroom.md) contracts:

- Posting samples are 5a's retained `traffic` snapshots with their `OffsetNS`
  scrape-dispatch offsets; `boundary` and `drain` are not posting.
- Planned duration is `Protocol.WindowSeconds`; configured capacity is
  `Effective.Metrics.scheduler.registration_limit`, never observed occupancy.
  Missing/nonpositive capacity rejects full windows.
- Length/age are retained `pending`/`oldest_pending_age_ns` scheduler gauges
  ([definitions](dev-metrics.md#scheduler-snapshot)); missing, null, negative or
  unparseable gauges reject full windows. Gauge decreases are valid; pool
  counter resets belong to headroom, not this rule.
- #867 and the old private evaluator supply completeness: posting boundaries
  within inclusive 2 s, nonnegative strictly increasing offsets, gaps ≤2 s,
  ≥8 samples in every planned half-open 10 s bucket, including warm-up.

Means weight posting samples equally and exclude half-open endpoints. Sliding
adjacent 120 s comparisons start at 60 s, advance 60 s and fit planned duration;
cumulative compares [60,240) with [420,600). Either metric's rise strictly above
max(5% of earlier mean, floor) rejects: length floor is 0.5% of configured capacity,
age floor is 10 ms. Equality passes. Measured evidence retains means/tolerances,
coverage failure offsets/buckets, duration, bound, posting count and completeness.
Positive 10/30 s diagnostic spikes slide 1 s after warm-up, retain each metric's
earliest maximum offset and never change verdicts; no positive rise gives zero/zero.

The evaluator expands `testdata/stage2-stability.json` ranges without sorting
and asserts completeness/verdicts formerly shared with the removed private
evaluator ([history](sidebar-counts-history.md#stability-and-validation-plans)). Tests use virtual offsets; mutation commands
and failing assertions stay in the local PR body. No runner or load execution.
