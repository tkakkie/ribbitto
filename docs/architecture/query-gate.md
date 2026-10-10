# Query gate

Two tools-module tests check every production query in `db/queries/`:
[`tablecheck`](../../tools/tablecheck/table_test.go) for table ownership and
the expressions a query may use, and
[`scopecheck`](../../tools/scopecheck/check_test.go) for organisation scope.
This document states the rules both apply to production queries, once.
Table ownership, read exemptions and the migration allowlists are in
[import checks](import-checks.md); migrations and `sqlc` are in
[database development](../database.md).

## Shared infrastructure

`make check` runs tools tests uncached (`go -C tools test -race -count=1 ./...`).
Both use [`tools/internal/sqlwalk`](../../tools/internal/sqlwalk/doc.go) for
`module.QueryName` query loading, unnamed SQL and duplicate rejection, `pg_query_go`
JSON parsing, statement/CTE/subquery traversal and the FROM `unnest` permission
rule. Organisation scope and table ownership policies stay in their respective
checkers. Shared reasons
reject pending approval and require issue, PR-comment or numbered decision
provenance for maintainer claims (not approval verification). The parser needs
cgo and a C compiler (Xcode locally, GCC on CI).

## Organisation scope (scopecheck)

Scopecheck requires each owned table's own WHERE scope in SELECT, UPDATE and DELETE,
including CTE bodies; joins never carry scope. UPDATE may not assign the scope
column (`organization_id`, or `id` for `organization`). Ownership comes from migration
columns, with `organization` scoped by `id` and an explicit installation-wide
list; unknown ownership, scoped list entries (except singleton `setup`) and stale
entries fail.

Plain INSERT VALUES and INSERT SELECT with at most one source pass:
a same-statement CTE or a FROM `unnest` of parameters (below). Each joined
table needs its scope. INSERT refuses subqueries in VALUES, RETURNING or SELECT,
joins (including comma joins), physical-table SELECT reads, ON CONFLICT and other
shapes; full checking is a follow-up.

CTE reads need no scope; LEFT/RIGHT/FULL joins and set operations fail.
Reads accept FROM SELECTs and CROSS JOIN LATERAL or inner JOIN LATERAL
SELECTs ON true (#741): ORDER BY/LIMIT bounds per-channel/topic counts. Each
nested table needs its SELECT's WHERE scope, never ON. Derived aliases cannot
shadow tables; derived INSERT/UPDATE/DELETE sources fail. Tablecheck's
ownership walk is unchanged for them.

`tools/scopecheck/allowlist.txt` uses `module.QueryName reason…`; stale, unnecessary
and `PENDING MAINTAINER:` entries fail (case-insensitive, with any non-alphanumeric
separator between the marker words).

## Expressions (tablecheck)

Query expressions accept only `sqlc.arg`, `sqlc.narg`, `count`, `max` and `lower`.
Query operators allow unqualified `=`, `<`, `>`, `<=`, `+`; casts allow
unqualified `uuid`, `bigint`, `jsonb`.

Tablecheck accepts scalar `sqlc.arg(name)::int8multirange` (#743), with
identifier/string names, and unqualified `@>` only with that exact cast on the
left and a column or accepted expression cast to scalar `bigint` on the right
(sqlc resolves column types). `NOT (sqlc.arg(read_set)::int8multirange @> m.event_seq)`
excludes the read set for #746's counts and #750's candidates. Qualification,
array bounds, typmods, `sqlc.narg`, range constructors/aggregates and other
range types/operators fail. Tablecheck still rejects foreign writes.

## Parameter relations (both gates)

Both gates accept unqualified FROM `unnest` of `sqlc.arg(name)::bigint[]` or
`sqlc.arg(name)::uuid[]` parameters, single or mixed arrays, in SELECT, UPDATE
FROM, CTEs and INSERT SELECT. Names are identifiers/strings; casts are
unqualified with one unsized dimension; output columns may be aliased. Bounds
enable set-based writes (#727), IDs counts (#740). The relation reads parameter
values, not tables, so it needs no scope; each table joined to it still does.

WITH ORDINALITY (#749) requires one array and an alias naming two columns:
`unnest(sqlc.arg(x)::bigint[]) WITH ORDINALITY AS a(value, n)` (or `uuid[]`).
SELECT, UPDATE FROM, CTEs and INSERT SELECT accept it. Parallel arrays join on
their ordinals; INSERT SELECT's single-source rule puts that join in a CTE. The
pinned sqlc v1.31.1 refuses multi-argument `unnest`
(`function unnest(unknown, unknown) does not exist`), hence the ordinal join.
Go must reject parallel arrays of unequal length before the statement runs:
an ordinal join silently drops unmatched positions.

Other FROM functions, unnest arguments except unqualified, unsized `bigint[]`
or `uuid[]` `sqlc.arg` parameters and unnest outside FROM fail, as do explicitly
LATERAL function calls, ROWS FROM, multi-argument WITH ORDINALITY and column
type definitions.
