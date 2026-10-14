# Write-time unread counters: SQL adapter (#957a–c2a)

The test-only adapter in `internal/realtime/unread_counters_sql_test.go` creates
scoped bigint totals and complete member bundles with validity/revision. It uses
organisation-first locks inside production post/read transactions, then counts
messages in exact newly covered ranges by topic before COMMIT. Own posts enter
that difference once; range/floor changes advance revision. Materialisation
counts every topic uncapped. Missing/invalid bundles fall back to SQL prefix/range
counts in the caller's snapshot. Raw fallback is exhaustive for verification;
#957c3 must retain today's capped fallback separately for measurement.

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

`internal/realtime/unread_counters_intervals_test.go` adds #957c1's
**measurement-intended shape (unmeasured)**. Feed, cursorless post and notice
writers retain `MergeCovered`'s newly covered intervals under the source lock.
Each interval uses a scoped half-open `event_seq` lookup supported by
`message_channel_event_seq_idx`, grouped by current topic; widths never count
messages. Topic reads retain the one-per-unread-message candidates before
coalescing; topic-cursor posts add their own message once, outside the old
cursor. Successful floor INSERT/UPDATE effects advance revision; repeated
reads leave it unchanged. Captures are transaction-local, including on retry.
Move transfers keep #957b's every-member locking.

The **correctness-only shape** retains the whole-coverage difference and channel
scan. Both shapes run the same sequential oracle and gated transaction tests,
including no-ops, overlap, rollback, retries, prefixes, missing/invalid bundles
and pinned snapshots. A structural guard rejects whole-coverage aggregation
on the intended path. Materialisation and exhaustive fallback remain correctness
checks; matched capped recounts and workload wiring follow in #957c3–c4.

`internal/realtime/unread_counters_accounting_test.go` adds #957c2a's
**unmeasured accounting seam**, shared by current production writes and intended
counter writes. An explicit operation context counts application statements,
transaction controls separately (including savepoints), and successful row
mutations, including DELETE RETURNING. SELECT/locking results are not mutations.
Maintenance CTEs return the sum of all their writes' RETURNING rows in their
existing round trip; no extra observation query or coverage scan is added.
Repeated reads may still physically rewrite ranges even with no unread change.
Rows include work later rolled back, rather than only committed changes.

An injected clock spans BEGIN through COMMIT/ROLLBACK, including nested
maintenance and failed BEGIN, excluding pool acquisition and post-transaction
work. Per-operation samples yield p50/p95/p99 with the harness's percentile
convention. The caller records each failed attempt once, each deadlocked attempt
once (SQLSTATE 40P01), and actual retry attempts explicitly; no retry policy is
introduced. Fresh write-owned captures are still required for retries.
Deterministic tests use virtual time and exact SQL/mutation expectations for
posts, feed/topic reads, repeated reads, moves with notices, and lazy joins in
both designs. Fixtures/oracles use a separate untraced pool/context; the seam
rejects correctness-only coverage SQL. Lock-wait observation, per-move breakdown
and gated contention accounting remain #957c2b.

Production, migrations and #867's stability rule are unchanged. No measurements
or adoption conclusion follow from these tests. Only the intended maintenance
shape may contribute measured evidence in a later approved run. The whole-
coverage shape and #956c's bytea carrier never do.
