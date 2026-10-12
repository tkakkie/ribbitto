# Sidebar queue stability criterion and evidence (#832, parts 1–2 of 3)

Fixed before new measurement by the maintainer on 2026-10-12. This changes only
the test harness. Part 2 completes the regression evidence below. The [part-3 stage-1 plan](stream-sidebar-stage1.md) adds configurations and the
runner; all three parts must land before measurement. No activation is implied.

The [recorded results](stream-sidebar-arms-measured.md) were decided by the old
terminal guard: three strictly increasing ten-second means in the last thirty
seconds rejected either metric. Those verdicts stand, including the 25–30% rise;
they are not reclassified as noise. The guard is superseded for new runs, with
no last-30-s or last-60-s automatic rejection.

## New window rule and telemetry

Recommendation requires all three independent 600-second windows of the stage-1
matrix to pass; the [stage-1 runner](stream-sidebar-stage1.md#matrix-and-commands)
aggregates them. `TestStreamCost` logs only per-window
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

Logs report `queue telemetry: complete=<bool> duration=<duration> bound=<bound>`
even for an empty series, distinguishing incomplete coverage from growth without
changing any verdict. They retain snapshots and counters, add sliding/cumulative
means and the verdict,
and report each metric's largest positive rise between adjacent 10-second and
30-second means, sliding by one second after warm-up. Offsets identify the earlier
period's start (earliest tie wins); zero means no positive rise. Spikes are
cause-analysis telemetry, not a verdict. `costTargets` remains unchanged.

## Focused evidence

Focused tests use deterministic offsets. Existing growth and eligibility cases
now use 600 seconds; short windows require inconclusive. `terminal-spike`
passes for last-three-ten-second means 1→2→3 under the new mean rule.

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

## Complete regression evidence (part 2)

`TestCostStabilityEvidence` uses 600 one-second posting samples, without sleeps.
The plateau alternates length 875/1,125 and age 175/225 ms every ten seconds:
a 28.6% low-to-high swing, with flat 1,000/200 ms means. It uses the archived
`w-5-2.log` swing scale; historical growth verdicts stand.
Length-only and age-only ramps reject; length fixed at its 5,000-registration
bound with age increasing also rejects. The slow length/age ramps rise by
1 entry / 2 ms per minute: every sliding rise is 2 entries / 4 ms, within
the floors, but cumulative rises are 6 entries / 12 ms and reject. A rise lasting the final
60 seconds rejects. Each checks recommendation eligibility with passing latency.

`TestCostStabilityEvidenceBoundaries` checks below, at, and above each limit
(one entry / one nanosecond apart), through full-window means:
length floors 5 and 25 for bounds 1,000 and 5,000; length percentage 100 at
mean 2,000; age floor 10 ms; age percentage 15 ms at mean 300 ms.
`TestCostStabilityEvidenceCoverage` requires inconclusive at 0/60/300/599 seconds
and rejection for nil, late/early coverage, a warm-up gap and invalid bound.
The existing matrix still checks all three legacy arms, gaps, bucket density,
duplicates, ordering, negative offsets, overrun and drain.

### Archived 300-second replay

Source: local measurement ref `origin/measurements/832-2026-10-12`, commit
`e76cb4c922306fe938daa61b3da63ebab22eebdb`. No fetch or new measurement ran.
Extract the six logs into a scratch directory, preserving their full contents:

```sh
archive_dir=$(mktemp -d)
for rate in 5 20; do
  for round in 1 2 3; do
    git show e76cb4c922306fe938daa61b3da63ebab22eebdb:five-minute/w-$rate-$round.log > "$archive_dir/w-$rate-$round.log"
  done
done
RIBBITTO_STABILITY_ARCHIVE="$archive_dir" go test -race -count=1 -v \
  -run '^TestCostStabilityArchiveReplay$' -timeout 2m ./internal/realtime
rm -rf "$archive_dir"
```

Replay passed all six logs with repository-local Go caches, both from extracted
logs and the read-only measurement archive. It preserves offsets/phases and
asserts complete 300-second coverage, inconclusive/ineligibility and every rise
and offset below. Ages are in ms; rises round to three decimals. Absolute rise
tolerances are 0.000500001 entries / 500.001 ns (half the last decimal plus float
rounding). Offsets match exactly: length / age, at the earlier period's start.

| Window | 10 s rise length / age | Offsets | 30 s rise length / age | Offsets |
|---|---|---|---|---|
| w-5-1 | 237.300 / 71.661 | 3m30s / 3m30s | 120.333 / 36.775 | 2m10s / 2m10s |
| w-5-2 | 274.600 / 78.341 | 2m36s / 2m36s | 194.167 / 59.282 | 3m11s / 3m11s |
| w-5-3 | 237.600 / 73.836 | 2m2s / 2m2s | 149.133 / 42.078 | 2m26s / 2m26s |
| w-20-1 | 285.700 / 114.357 | 1m30s / 1m31s | 208.267 / 88.803 | 1m11s / 1m11s |
| w-20-2 | 296.100 / 136.591 | 4m40s / 4m40s | 153.433 / 88.772 | 3m23s / 3m23s |
| w-20-3 | 350.800 / 118.017 | 4m4s / 2m4s | 233.133 / 85.651 | 3m44s / 3m36s |

### Part-2 mutation runs

Each isolated mutation ran, exited 1 at the quoted assertion, and was restored.
Commands use the same
`go test -race -count=1 -run '<selector>' -timeout 2m ./internal/realtime`
form as part 1. Replay also sets `RIBBITTO_STABILITY_ARCHIVE` as above.
Selectors in this table have the prefix `^TestCostStability` and suffix `$`.

| Selector suffix | Mutation | Actual failing assertion |
|---|---|---|
| `Evidence/oscillating-plateau` | Veto when 10 s length spike > 5 | `stability = reject, want pass` |
| `Evidence/length-only` | Zero length rise | `stability = pass, want reject` |
| `Evidence/age-only` | Zero age rise | `stability = pass, want reject` |
| `Evidence/capped-length-growing-age` | Pass if earlier length mean ≥ bound | `stability = pass, want reject` |
| `Evidence/slow-length` | Disable cumulative comparison | `stability = pass, want reject` |
| `Evidence/slow-age` | Disable cumulative comparison | `stability = pass, want reject` |
| `Evidence/late-rise` | Omit offsets ≥ 540 s from means | `stability = pass, want reject` |
| `EvidenceBoundaries/length-floor-1000` | Zero length floor | `delta=-1 stability = reject, want pass` |
| `EvidenceBoundaries/length-floor-5000` | Hard-code bound 1,000 | `delta=-1 stability = reject, want pass` |
| `EvidenceBoundaries/length-percentage` | Zero length percentage | `delta=-1 stability = reject, want pass` |
| `EvidenceBoundaries/age-floor` | Zero age floor | `delta=-1 stability = reject, want pass` |
| `EvidenceBoundaries/age-percentage` | Zero age percentage | `delta=-1 stability = reject, want pass` |
| `EvidenceBoundaries/length-floor-1000` | Length `>` → `>=` | `delta=0 stability = reject, want pass` |
| `EvidenceBoundaries/age-floor` | Age `>` → `>=` | `delta=0 stability = reject, want pass` |
| `EvidenceCoverage` | Short window returns pass | `duration=0s stability = pass, want inconclusive` |
| `ArchiveReplay` | Minimum duration 300 s | `archived 300s stability = pass, want inconclusive` |
| `ArchiveReplay` | Spike width +1 s | `width=10s length offset=3m29s, want 3m30s` (`w-5-1`) |
| `ArchiveReplay` | Spike slide 1 s → 2 s | `width=30s length offset=3m10s, want 3m11s` (`w-5-2`) |
| `EvidenceCoverage/warm-up-gap` | Bypass completeness | `incomplete stability = pass, want reject` |
| `EvidenceCoverage/invalid-bound` | Remove positive-bound check | `incomplete stability = pass, want reject` |

Eight part-1 mutations were rerun: warm-up start 0, disabled sliding,
120 s slide, spike offset +1 s, including drain in means, seven-sample buckets,
unbounded posting end, and bypassing stability eligibility. Each exited 1 with
the same assertion already quoted in the focused evidence table.
