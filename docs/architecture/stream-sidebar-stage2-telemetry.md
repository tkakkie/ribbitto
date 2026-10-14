# Stage-2 continuous telemetry (#865, PR 3 slice 5a)

The [fixed protocol](stream-sidebar-stage2.md) has a sampler and pool aggregates
in `cmd/loadgen/stage2_telemetry.go`; the [headroom evaluator](stream-sidebar-stage2-headroom.md)
consumes this contract. The sampler adds no runner or gate verdicts.
The caller supplies a clock, context-aware wait and metrics reader; tests use
virtual time. The HTTP reader uses the loopback transport, a one-second deadline,
one attempt and a 1 MiB cap. Every successful snapshot retains all six sections.

Slots are anchored at seconds 0–599, with a separate terminal `boundary` at 600.
Offsets record scrape dispatch. Errors and missed slots are returned alongside
retained samples; a late scrape never backfills expired slots or duplicates an
opening scrape while catching up. Drain is excluded. Aggregation accepts
`traffic` only in `[0,600s)` and `boundary` only at the closing boundary,
`[600s,602s]`; a wrong phase invalidates the evidence.
A valid terminal snapshot is retained if cancellation occurs during its fetch,
but the sampler still returns a sampling-stopped error.

The [stability protocol's](stream-sidebar-stability.md#new-window-rule-and-telemetry)
inclusive two-second tolerance applies to the whole window's start and end,
including those endpoints of the first and last minute buckets. Sampling accepts
an opening dispatch at offsets `[0,2s]` and a closing dispatch at `[600s,602s]`;
it waits for the planned boundary and never dispatches early. Aggregation uses
the same endpoint limits. Missing traffic seconds remain missing even when the
opening boundary is covered. Boundary coverage alone does not establish
completeness: checks for gaps of at most two seconds and at least eight samples
in every planned ten-second bucket remain in the stability evaluator in slice 5c.

Whole-window and all ten planned 60 s aggregates retain sample/missing counts,
pool counter counts and durations (with cancellations separate), reset offsets,
mean empty wait and waiting pressure. Internal minute boundaries retain the
stricter scheduled-second condition: dispatch in `[B,B+1s)`, so exactly one
second late is a miss. This keeps bucket counter endpoints in the intended
sampling slot rather than borrowing a later slot to hide missing evidence.
A miss at either endpoint makes that bucket's `BoundaryCovered` false, leaves
deltas absent and elapsed time/ratios at their unavailable zero defaults. It
cannot produce a pass; slice 5b's consumer must treat unavailable bucket ratios
as inconclusive. Gate verdicts belong to the consumer, not aggregation.

Elapsed time uses actual endpoint offsets. Interior counter drops remain visible
even when net deltas grow. A counter drop into the end sample belongs to the range
only within its end-boundary tolerance; at an internal minute boundary, a drop
into the first sample at or after `StartNS + 1s` belongs to the following range
when a previous retained sample exists, while on-time drops stay in the earlier range.
Missing boundaries leave deltas absent; resets suppress
derived ratios. Zero empty acquisitions give zero mean wait. Consumers must check
coverage, missing samples, resets and counter consistency before interpreting ratios.
`SampledMaxNotExactPeak` labels pool occupancy maxima explicitly: these are sampled
maxima, not exact peaks. Cumulative waits cannot establish wait percentiles.
