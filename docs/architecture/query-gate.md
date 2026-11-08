# Query gate

Two tools-module tests check every production query in `db/queries/`:
[`tablecheck`](../../tools/tablecheck/table_test.go) for table ownership and
the expressions a query may use, and
[`scopecheck`](../../tools/scopecheck/check_test.go) for organisation scope.
Table ownership, read exemptions and the migration allowlists are in
[import checks](import-checks.md); migrations and `sqlc` are in
[database development](../database.md).

## Shared infrastructure

`make check` runs tools tests uncached (`go -C tools test -race -count=1 ./...`).
Both use [`tools/internal/sqlwalk`](../../tools/internal/sqlwalk/doc.go) for
`module.QueryName` query loading, unnamed SQL and duplicate rejection, `pg_query_go`
JSON parsing, statement/CTE/subquery traversal and the FROM `unnest` permission
rule. Organisation scope and table ownership policies stay in their respective
checkers. Reasons
reject pending approval and require issue, PR-comment or numbered decision
provenance for maintainer claims (not approval verification).

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

CTE reads need no scope; LEFT/RIGHT/FULL joins fail.
Reads accept FROM SELECTs and CROSS JOIN LATERAL or inner JOIN LATERAL
SELECTs ON true (#741): ORDER BY/LIMIT bounds per-channel/topic counts. Each
nested table needs its SELECT's WHERE scope, never ON. Derived aliases cannot
shadow tables; derived INSERT/UPDATE/DELETE sources fail. Tablecheck still
checks their ownership.

Read SELECTs accept `UNION ALL` at the top level and inside derived relations
(#742). Every branch's tables
need that branch's own WHERE scope, never ON or an outer predicate. `UNION`
(distinct), `INTERSECT` and `EXCEPT` (including ALL) fail with named errors.
Any set operation in a statement containing INSERT, UPDATE or DELETE,
including CTE sources or writes, also fails; this permission applies only
to reads. Tablecheck's ownership
walk validates every branch, including nested ones, as a SELECT and refuses
SELECT INTO there too; both gates' other refusals are unchanged.

`tools/scopecheck/allowlist.txt` uses `module.QueryName reason…`; stale, unnecessary
and `PENDING MAINTAINER:` entries fail (case-insensitive, with any non-alphanumeric
separator between the marker words).

## Expressions (tablecheck)

Query expressions accept only `sqlc.arg`, `sqlc.narg`, `count`, `max`, `lower`
and `uuidv7` (the drafts horizon, #1178).
Query operators allow unqualified `=`, `<`, `>`, `<=`, `+`. Operator names
are checked in comparisons, quantified ANY/ALL subqueries and ORDER BY USING
sort clauses alike; unclassifiable names fail. IN subqueries, EXISTS, scalar
subqueries and plain/DESC sorts need no explicit operator. Casts allow
unqualified `uuid`, `bigint`, `jsonb`.

Tablecheck accepts scalar `sqlc.arg(name)::int8multirange` (#743), with
identifier/string names. Unqualified `@>` requires that cast or the qualified
value column of an ordinal `int8multirange[]` parameter relation below (#2182)
on the left. The latter must resolve directly in the same statement, never
through a CTE/derived output or a correlated reference. Ordinary table columns,
uuid/bigint values and ordinal columns fail. The right side is a column (sqlc
resolves its type) or an accepted expression cast to scalar `bigint`.
Qualified casts/operators, typmods, `sqlc.narg`, range constructors/aggregates,
other range types/operators (`<@`, `&&`, etc.) and non-bigint right casts
(`uuid`, `int`, `text`, range, array) fail. Foreign writes still fail.

## Parameter relations (both gates)

Both gates accept unqualified FROM `unnest` of `sqlc.arg(name)::bigint[]` or
`sqlc.arg(name)::uuid[]` parameters, single or mixed arrays, in SELECT, UPDATE
FROM, CTEs and INSERT SELECT. Names are identifiers/strings; casts are
unqualified with one unsized dimension; output columns may be aliased.
Parameter relations need no scope; each joined table still does.

WITH ORDINALITY (#749, #2182) requires one array and an alias naming two columns:
`unnest(sqlc.arg(x)::bigint[]) WITH ORDINALITY AS a(value, n)` (also `uuid[]`
or `int8multirange[]`). The multirange array is accepted only in this form:
unqualified cast, one unsized dimension, no typmods, `sqlc.arg` parameter only.
SELECT, UPDATE FROM, CTEs and INSERT SELECT accept it. Parallel arrays join on
their ordinals; INSERT SELECT's single-source rule puts that join in a CTE. The
pinned sqlc v1.31.1 refuses multi-argument `unnest`
(`function unnest(unknown, unknown) does not exist`), hence the ordinal join.
Go must reject parallel arrays of unequal length before the statement runs:
an ordinal join silently drops unmatched positions.

Other FROM functions, non-parameter arrays (columns, ARRAY, subqueries, function
results), qualified calls/casts and unnest outside FROM fail, as do explicitly
LATERAL calls, ROWS FROM, multi-argument WITH ORDINALITY and column definitions.
Other range arrays (`int4multirange[]`, `int8range[]`, `tsrange[]`,
`nummultirange[]`, etc.) and multirange arrays without ordinality fail, including
multi-argument unnest containing a multirange array.
