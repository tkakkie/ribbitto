# Sidebar queue stability criterion (#832, part 1 of 3)

Fixed before new measurement by the maintainer on 2026-10-12. This changes only
the test harness. Parts 2 (complete regression evidence) and 3 (stage-1
configuration and plan) must follow before measurement; no activation is implied.

The [recorded results](stream-sidebar-arms-measured.md) were decided by the old
terminal guard: three strictly increasing ten-second means in the last thirty
seconds rejected either metric. Those verdicts stand, including the 25–30% rise;
they are not reclassified as noise. The guard is superseded for new runs, with
no last-30-s or last-60-s automatic rejection.

## New window rule and telemetry

Recommendation requires all three independent 600-second windows of the stage-1
matrix to pass; part 3 aggregates them. `TestStreamCost` logs only per-window
`window-stable=pass|reject|inconclusive` and `window-eligible=true|false`, which
cannot establish recommendation eligibility alone. Each window excludes `[0,60s)`
from trend judgement. Compare adjacent two-minute means `[s,s+120s)` and
`[s+120s,s+240s)`, starting at 60 seconds and sliding by 60 seconds while both
fit the planned posting duration. Compare minutes 2–4 (`[60s,240s)`) with
8–10 (`[420s,600s)`) too. Means weight posting samples equally; drain and
half-open endpoints are excluded. Either comparison rejects if either rise
is greater than its tolerance (equality passes):

- Length: max(5% of the earlier mean, 0.5% of the registration bound).
- Oldest pending age: max(5% of the earlier mean, 10 ms).

The bound is the queue's configured registration capacity, carried by the step
result, never its observed length. Windows shorter than 600 seconds report
`inconclusive` and cannot be recommendation-eligible. Missing/invalid telemetry
in a full window rejects. The 1aeb479 completeness rule remains: posting start
and end within two seconds of the planned boundaries, nonnegative strictly
increasing offsets, gaps at most two seconds, and at least eight samples in
**every planned ten-second bucket**, including warm-up. Drain cannot repair gaps.

Logs retain snapshots and counters, add sliding/cumulative means and the verdict,
and report each metric's largest positive rise between adjacent 10-second and
30-second means, sliding by one second after warm-up. Offsets identify the earlier
period's start (earliest tie wins); zero means no positive rise. Spikes are
cause-analysis telemetry, not a verdict. `costTargets` remains unchanged.

## Focused evidence

All new rule tests synthesize offsets without sleeps. The existing growth test
now uses 600 seconds and millisecond age growth. The existing telemetry test's
`stable` and tolerated-boundary assertions still require eligibility, now at
600 seconds instead of 60; short windows instead assert `inconclusive`.
The old 60-second ramp rejection in `TestCostQueueGrowthDisqualifies` now
checks a 600-second sustained ramp with the same failing assertion.
`terminal-spike` requires pass for last-three-ten-second means 1→2→3, replacing
the abolished automatic-rejection expectation with the new mean/tolerance rule.

Mutation commands below were actually run with isolated edits, then restored.
Each exited 1 at the quoted assertion. Prefix every selector with:
`go test -race -count=1 -run` and append `-timeout 2m ./internal/realtime`.

| Selector | Mutation | Actual failing assertion |
|---|---|---|
| `'^TestCostStabilityRules/warm-up$'` | Start sliding at 0 | `stability = reject, want pass` |
| `'^TestCostStabilityRules/sliding$'` | Disable sliding comparisons | `stability = pass, want reject` |
| `'^TestCostStabilityRules/cumulative$'` | Disable cumulative comparison | `stability = pass, want reject` |
| `'^TestCostStabilityTolerances$'` | Hard-code bound 1,000 | `registration-bound length tolerance changed` |
| `'^TestCostStabilityTolerances$'` | Zero length percentage, age floor, or age percentage (separately) | `exact tolerance rejected` |
| `'^TestCostStabilityTolerances$'` | Require both metrics (`&&`) | `registration-bound length tolerance changed` |
| `'^TestCostStabilityRules$'` | Short window returns pass | `short window = pass, want inconclusive` |
| `'^TestCostStabilitySpikes$'` | Add one second to spike offset | `spike width=10s rise=[20 4e+07] offsets=[1m51s 1m51s]` |
| `'^TestCostRecommendationRequiresTelemetry/async40/thin-bucket$'` | Require seven samples | `recommendation eligibility = true, want false for thin-bucket arm telemetry` |
| `'^TestCostRecommendationRequiresTelemetry/async40/posting-overrun$'` | Compare end upper bound against observed end | `recommendation eligibility = true, want false for posting-overrun arm telemetry` |
| `'^TestCostQueueGrowthDisqualifies$'` | Bypass stability in eligibility | `growing backlog accepted despite passing latency` |
| `'^TestCostStabilityRules/terminal-spike$'` | Restore terminal strict-growth veto | `stability = reject, want pass` |
| `'^TestCostStabilityRules/sliding$'` | Slide by 120 seconds | `stability = pass, want reject` |
| `'^TestCostStabilitySpikes$'` | Include drain in means | `spike width=10s rise=[927.2727272727273 3.636363636363637e+07] offsets=[1m50s 1m50s]` |
| `'^TestCostStabilitySpikes$'` | Omit 30-second spikes | `spike width=30s rise=[0 0] offsets=[0s 0s]` |
