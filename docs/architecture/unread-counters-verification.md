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

## 956b: sequential production writers (956b1)

`internal/realtime/unread_counters_matrix_test.go` reuses the prototype with
production `Posting`, `Reading`, setup and sign-up, through exported wiring.
Test-local adapters reproduce the package-local composition in
`cmd/ribbitto/read_adapters.go`, including topic-post preparation reuse. Only
channel/topic metadata uses fixtures; no message or read state is written with
direct SQL. Maintenance uses the previous snapshot and operation arguments and
results, never the oracle or a post-write recount.

Every operation compares the full model, raw counts and capped badges with the
oracle in one read-only repeatable-read snapshot. Cases cover feed/topic posts
with a shown cursor, cursorless older pages and own messages; fragmented,
overlapping, repeated, reordered, floor-only and no-op feed/topic requests from
several simulated tabs; implicit joins before read rows; missing bundles;
99 → 100 → 101 → 100 → 99 → 0 → 1 transitions. The comparisons include lists,
a listed selection, another empty channel, the materialised owner and an
unmaterialised member (plus a late join). Separate assertions check fragmented
ranges, unchanged floors on cursorless posts and the floor-only write.

Eight mutation checks are recorded in the pull request.

## 956b2: production moves and unlisted scopes

`internal/realtime/unread_counters_moves_test.go` extends the same matrix with
production branching into existing and new topics, including source notices.
Transfers preserve differing read sets for two materialised members and a member
without a bundle. Cases move old messages below a destination's floor, read
before/at/after move cursors, reorder reads and move a message again. An unlisted
branch reaches raw 100 (`99+`) then zero; channel totals include an unlisted
sibling. Full-model comparisons after every operation include bounded lists,
the unlisted selection, a nonempty other channel and a foreign organisation with
a nonempty unread sentinel. One account has memberships in both organisations.
Foreign fixtures supply only metadata; every message/read mutation uses the
production writers. The branch adapter captures the notice sequence from its
production read arguments; maintenance still uses the pre-write snapshot.

## 956c: PostgreSQL transactions and ordering

`internal/realtime/unread_counters_transactions_test.go` runs production topic
reads, page posts, branches and sign-up with a test-only adapter. For read/move,
post/read and join/post, both orders run with both commits and with either
operation aborted then retried as a whole transaction: 18 gated cases. Both
transactions begin before writes; `pg_blocking_pids` confirms the second waits
on the first's organisation lock. Gates control release; deadlines only bound
failures, with no sleeps or measurements.

**PostgreSQL evidence:** the adapter takes the design's conservative organisation
lock before loading pre-write truth. It stores the prototype's uncapped totals
and complete bundles in one test-only bytea row on the production transaction's
connection, before COMMIT. A server error after both writes confirms rollback;
source messages, members, ranges, floors, event sequence, event/account counts
and counter bytes stay unchanged. Retry reloads committed truth and counters
after acquiring the lock. This tests READ COMMITTED classification and atomicity
under the proposed protocol; today's production reads do not take this extra
lock. The bytea row is a test carrier, not a production schema or fan-out test.

**Arithmetic evidence:** the existing in-memory maintenance uses pre-write truth,
arguments and production event results, never oracle results or a post-write
recount. Post/read shares one member's implicit and explicit reads. Initial
bundles include both differently read members; joining eagerly
materialises a test bundle from its prefix. At every outcome and while writes
are uncommitted, persisted raw totals/read counts are checked separately, and
all members' full models match the independent oracle, including raw counts and
99+ badges. One read-only repeatable-read snapshot is reread across each commit
or rollback and must retain the same model; fresh snapshots check the outcome.

Together with 956a/956b, this completes #956's correctness cases. #957 may start
after 956c completes; adoption remains undecided. Cost bounds are **estimates**;
no load run or measurement ran.
