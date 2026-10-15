# Recount operations

Part 2c adds command-local [explicit capacity configuration](../database.md#recount-capacity-configuration).
`RIBBITTO_ASYNC_RECOUNTS` defaults to off (also for empty); only on and off
are accepted. Off ignores stray budget inputs and leaves pgx pool behavior
unchanged. On requires explicit C/W/M/R/E/P/H without defaults and validates
`B = floor((M-R-E)/P)`, `0 < C <= B` and `0 < W <= C-H` before workers or
listeners start. C overrides the URL's pool maximum; registration capacity
comes from the effective process stream cap, with no extra setting.
M=100, R=3, E=20, P=2, H=10 gives B=38: C=36/W=26 passes, while
C=39 or W=27 fails. **36/26 is a validation configuration, not a production
default.** Startup logs non-secret effective capacity once. Part 2h composes
opt-in delivery; [operation and validation status](../database.md#recount-capacity-configuration).

Part 2f forwards optional `RecountObserver` through Stream and web Streaming,
including RunReserved; nil adds no clocks, allocations or locks. Aggregates
measure queue, compute, receipts, return→send and submit→send
([delivery metrics](dev-metrics.md#delivery-snapshot)). Stage 2 separates queue,
compute and send delay but cannot prove freshness; that stage was stopped (decision 34).

Part 2g closes an optional scheduler with HTTP shutdown, after `Hub.CancelAll`
and before the pool ([order](stream-limits.md#shutdown)).
