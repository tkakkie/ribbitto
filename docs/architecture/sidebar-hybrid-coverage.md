# Hybrid sidebar coverage verification (#961, part 2c-a)

The external realtime prototype adds a writer-side coverage guard to the
[contract](sidebar-hybrid-contract.md); production code and intervals are unchanged.
Snapshot computations and the independent oracle share one pinned repeatable-read
transaction. Gates commit posts and reads before capture, during computation and
before model application. Raw counts cross the display cap, and a later persisted
model must differ from the pinned oracle. Watch generations are captured before
pinning: later publications stay dirty even when their writes are visible.

The exact-delta arm is hypothetical: serial fixture commits provide complete
per-member effects, organisation sequences (including no-ops) and persisted read
revisions from [960b's receipts](sidebar-hybrid-hypothetical.md). It does not make
production invalidations derivable. Duplicates and old incarnations are ignored;
a missing predecessor repairs. A newer request may replace older persisted
coverage only after replaying the retained exact chain, bounded to eight inputs.
Eviction or missing read coverage preserves applied state and schedules a fresh
recount. Full-model equality includes scope, lists, selection, raw/display counts
and badges; generation assertions are separate from the oracle.

These are model-application checkpoints, not SSE sends. Disconnect cancellation,
authorization before every send, send ordering, reconnect recovery and equality
under continuous posting remain part 2c-b. Measurements and adoption remain #952.
