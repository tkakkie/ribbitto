# Stage-2 headroom evaluator (#865, PR 3 slice 5b)

`cmd/loadgen/stage2_headroom.go` consumes the [telemetry contract](stream-sidebar-stage2-telemetry.md)
and supplies exactly the five [pinned headroom verdicts](stream-sidebar-stage2.md)
to window evaluation. These gates are async-only, so sync windows record them as
inconclusive. The [stability evaluator](stream-sidebar-stage2-stability.md) supplies its separate gate.
Invalid phases, offsets or pool sections error before changing the window.

Acquired p95/p99 use exact nearest rank, and maximum uses retained `traffic`
samples in seconds 0–599. Terminal `boundary` and drain never count. The maximum
is explicitly labelled sampled, not an exact peak. Saturation divides at-C
observations by traffic samples, using C from the pinned arm named async;
consecutive observations must occupy adjacent planned seconds. A gap inside or
adjacent to a run cannot prove absence of three.

Empty-acquire and mean empty-wait gates check only the whole window; waiting
pressure checks the whole window and every planned minute using telemetry's
deltas, actual elapsed duration, mean empty wait and waiting pressure. Zero
acquisitions give zero fraction only with zero empty acquisitions and wait; zero
empty acquisitions give zero mean wait only with zero wait. Inconsistent counters
are inconclusive. Counts, durations and cancellation deltas remain in measured evidence.

Uncovered boundaries, missing seconds, resets or duplicate traffic seconds make
every gate needing that range inconclusive, even alongside observed exceedances.
Each verdict's measured keys mark `range_<start>_<end>s.inconclusive` and retain coverage,
missing seconds, sample count and reset count. Occupancy gates need the whole
window, as do empty-acquire and empty-wait; waiting pressure needs all eleven
ranges. No load execution is added.
