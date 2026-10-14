# Stage-2 continuous telemetry (#865, PR 3 slice 5a)

The [fixed protocol](stream-sidebar-stage2.md) has a sampler and pool aggregates
in `cmd/loadgen/stage2_telemetry.go`. It adds no runner or gate verdicts.
The caller supplies a clock, context-aware wait and metrics reader; tests use
virtual time. The HTTP reader uses the loopback transport, a one-second deadline,
one attempt and a 1 MiB cap. Every successful snapshot retains all six sections.

Slots are anchored at seconds 0–599, with a separate terminal `boundary` at 600.
Offsets record scrape dispatch. Errors and missed slots are returned alongside
retained samples; a late scrape never backfills expired slots. Drain is excluded.
Whole-window and all ten planned 60 s aggregates retain sample/missing counts,
pool counter counts and durations (with cancellations separate), reset offsets,
mean empty wait and waiting pressure. Boundaries must fall in their scheduled
second; elapsed time uses their actual offsets. Interior counter drops remain
visible even when net deltas grow. Missing boundaries leave deltas absent; resets
suppress derived ratios. Zero empty acquisitions give zero mean wait. Consumers
must check coverage, resets and counter consistency before interpreting ratios.
`SampledMaxNotExactPeak` labels pool occupancy maxima explicitly: these are sampled
maxima, not exact peaks. Cumulative waits cannot establish wait percentiles.
