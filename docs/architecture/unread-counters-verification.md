# Write-time unread counters: prototype verification (#956)

This note records what the test-only prototype of the
[counter design](unread-counters-design.md) has verified, part by part. It is
evidence for the [adoption criteria](unread-counters-adoption.md), not an
adopted architecture: production code, migrations and display rules are
unchanged.

## 956a: prototype and core verification

`internal/realtime/unread_counters_test.go` maintains uncapped channel and topic
totals and per-member read counts for posts (with and without a cursor), feed and
topic reads, moves, branch notices and lazy joins. It materialises bundles,
recounts when a bundle is absent, invalid or suspended, and detects and repairs
drift. The model under test reads raw persisted messages, join prefixes and
read ranges itself; it never uses the oracle's results.

`internal/realtime/unread_counters_core_test.go` compares that model with #950's
full oracle for two members (one without a bundle) after each core operation:
uncapped and capped counts with badges, list identity and order, an unlisted
selected topic and another channel. Both read the same read-only
repeatable-read snapshot, with no writer running.

What this shows and what it does not:

- **In-memory arithmetic only.** The persisted state in these cases is written
  by the test with direct SQL and `Writer.Merge`, following
  [unread writes](unread-writes.md), not by the production post, read, move and
  branch paths. A mismatch between those paths and the counter maintenance
  would not be caught here. 956b drives its cases through the production
  writers.
- **Drift.** A detected mismatch in the shared totals suspends every bundle of
  the channel, and read-count drift invalidates the member's bundle; both fall
  back to a recount, so no known wrong count is served after detection. Totals
  and read counts are checked separately, so equal errors cannot cancel. Drift
  can be served **before** it is detected; periodic checking cannot prevent
  that.
- **No transaction claims.** Commit order, locking, rollback and retry are 956c's
  PostgreSQL tests.
- **Cost.** The per-operation statement and row bounds in the pull request are
  model estimates for an unbatched adapter, not measurements.
