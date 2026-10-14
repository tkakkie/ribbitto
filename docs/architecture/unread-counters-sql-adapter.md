# Write-time unread counters: SQL adapter (#957a)

The test-only adapter in `internal/realtime/unread_counters_sql_test.go` creates
scoped bigint totals and complete member bundles with validity/revision. It uses
organisation-first locks inside production post/read transactions, then counts
messages in exact newly covered ranges by topic before COMMIT. Own posts enter
that difference once; range/floor changes advance revision. Materialisation
counts every topic uncapped. Missing/invalid bundles fall back to SQL prefix/range
counts in the caller's snapshot. Raw fallback is exhaustive for verification;
#957c must retain today's capped fallback separately for measurement.

`internal/realtime/unread_counters_sql_core_test.go` checks T/R separately and
compares raw/capped models with #950's oracle after every operation. Production,
migrations and #956c's bytea carrier are unchanged. Moves, notices, joins and
gated transaction histories remain #957b; measurements remain #957c–d. This
adapter provides no performance evidence or adoption conclusion.
