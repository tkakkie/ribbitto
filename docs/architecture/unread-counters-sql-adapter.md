# Write-time unread counters: SQL adapter (#957a–b)

The test-only adapter in `internal/realtime/unread_counters_sql_test.go` creates
scoped bigint totals and complete member bundles with validity/revision. It uses
organisation-first locks inside production post/read transactions, then counts
messages in exact newly covered ranges by topic before COMMIT. Own posts enter
that difference once; range/floor changes advance revision. Materialisation
counts every topic uncapped. Missing/invalid bundles fall back to SQL prefix/range
counts in the caller's snapshot. Raw fallback is exhaustive for verification;
#957c must retain today's capped fallback separately for measurement.

`internal/realtime/unread_counters_sql_moves_test.go` transfers topic T/R in the
production branch transaction, classifying selected original sequences by each
valid bundle's ranges and join prefix, ignoring destination floors. Channel T/R
change only for the source-topic notice, read only by its author. Missing bundles
stay absent. Sign-up takes the organisation lock and writes no counters; lazy
fallback and later whole-bundle materialisation include its committed prefix.

`internal/realtime/unread_counters_transactions_test.go` shares #956's gates
between its bytea carrier and the real SQL adapter. SQL histories cover both
orders of read/move, post/read and join/post, aborting either operation and
retrying after PostgreSQL confirms rollback. They include differing read states,
prefix-read moved messages, missing bundles, existing destinations below floors,
new unlisted destinations and materialised/missing notice authors. Each outcome
checks raw T/R/U and #950's raw/99+ model in a fresh snapshot; snapshots pinned
before outcomes are checked again afterward. Rollback compares source, counters,
validity/revision, topics, members, accounts, events and organisation sequence.

Production, migrations and #867's stability rule are unchanged. No measurements
or adoption conclusion follow from these tests. #957c must measure the design's
per-interval, index-backed counts from each write's newly covered intervals.
The whole-coverage difference and channel scan here remain correctness checks;
label both shapes, with only the intended shape counted as measured evidence.
