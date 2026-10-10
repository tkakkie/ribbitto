# Database development

**Keep it current:** update this file when configuration or the migration
workflow changes; the integration-test setup is in
[database tests](database-tests.md).

`RIBBITTO_DATABASE_URL` configures both the server and the explicit
`migrate up|down|status` command. The server checks connectivity but never
applies migrations. `.env.example` contains the local Compose URLs; export
them into the shell as shown in the README. `.env` is ignored by Git.

See [worktree environments](worktree-env.md) for per-worktree dev databases, ports, health checks and cleanup.

`make db-up` starts PostgreSQL 18 on loopback port 5432 and waits for its
health check. Its password is for development only. `make db-down` stops
it without deleting the named volume at `/var/lib/postgresql`.

`db/migrations` holds numbered goose SQL files embedded in the binary.
It is owned by `internal/platform/postgres`; only its packages and their
tests may import it. Other tests that need an older schema migrate through
`pgtest.NewMigrator`. The runner uses goose and pgx's
`database/sql` adapter without global configuration. The first
migration creates `organization`; goose also maintains its version table.

Migration 00007 adds realtime's `event_log`, keyed by `(organization_id, seq)`,
with `kind`, nullable `audience_member_id`, JSONB `data` and `created_at`.
The audience references `(organization_id, id)` in `member`; NULL means
organisation-wide. `data` holds only IDs ([payload shapes](architecture/realtime.md#durable-event-log)).
`organization.event_log_boundary_seq` is NOT NULL, defaults to 0, and is set
to each existing organisation's `event_seq` on upgrade: no event backfill.
Retention advances this replay boundary atomically with deletion. A deferred constraint trigger on
`organization` refuses, at commit, any raise of `event_seq` above the boundary
that leaves one of the sequences it took without an `event_log` row, so a server still running an older binary during
`migrate up` fails its write instead of leaving a gap. Down drops the trigger,
the log and the boundary.

Migration 00010 adds `organization.access_epoch` (bigint NOT NULL). Initial
values, existing rows and every bump come from the unowned installation-wide
`organization_access_epoch_seq`, so recreating an organisation cannot reuse
its epoch. `organization_access_guard` assigns fresh epochs on insertion, slug
changes and explicit epoch changes; it refuses changed organisation IDs and
decreasing epochs. `member_access_changed` bumps the organisation on deletion
or changed roles, accounts or organisations, and both organisations on a move;
it refuses changed member IDs. Joins and unchanged access values do not bump.
`member_access_truncated` bumps all organisations on member truncation,
including CASCADE; it has no separate refusal. Bumps share the write's
transaction, and Down removes the epoch objects. Writers lock organisations
before members (`SELECT … FOR NO KEY UPDATE`), taking both organisation locks
in ID order for moves, as in [posting](architecture/posting.md). Direct SQL in
the opposite lock order can abort with `40P01`; retry the whole transaction
using the organisation-first protocol. While ribbitto runs, `setval`, `ALTER
SEQUENCE … RESTART`, `SET session_replication_role = replica`, `ALTER TABLE …
DISABLE TRIGGER` and data-only `pg_restore --disable-triggers` are unsupported
because they can lower the epoch or bypass the triggers on which the guarantee
depends; for restore, [stop ribbitto first](../README.md#restoring-a-backup)
to clear caches.

Migration 00012 adds unread's `channel_read` lock rows, `read_range` bounds
and `topic_read_floor`. Composite foreign keys include `organization_id`
and enforce the member, channel and topic relationships; ranges and floors
reference their channel lock row. Down drops the three tables.

`RIBBITTO_EVENT_RETENTION` is a positive Go duration (default `168h`, seven
days; for example `24h`). The server cleans expired events once at start
and then hourly, with a one-minute timeout per run. It lists organisations
with expired rows in ID order without write locks, then processes one
organisation at a time, one transaction per batch. Each batch locks only that
organisation, then reads at most its lowest 1,000 sequences (the `(organization_id,
seq)` index) and deletes only the expired prefix among them: the rows from the
lowest sequence up to the first one that has not expired. If the lowest row
has not expired, the batch deletes nothing; if all 1,000 have, it deletes all
1,000. An expired row above an unexpired one stays until a later run, since
`created_at` need not follow `seq` and the boundary must never pass a row
still in the log (#430). The batch raises the replay boundary to the highest
sequence it deleted, then commits before the next batch. Errors and timeouts keep committed progress;
the next tick retries remaining work. The listing reads `event_log` once
(`created_at` has no index, so PostgreSQL scans the table; about 11 ms for
500,000 rows on a laptop, as the old organisation-scoped `EXISTS` did); no
additional index or migration is needed until the log grows far beyond that.
Messages and unread inputs are never deleted.

`RIBBITTO_MAX_STREAMS` caps open event streams per process (default `5000`
when unset). An explicit value must be a positive integer; empty, zero,
negative, unparsable and overflowing values fail at start. Excess streams
get 429 before streaming; the per-account cap remains 16. When
`RIBBITTO_AUTHORIZATION_CACHE_CAPACITY` is unset, the authorization cache
holds `max(10,000, this limit)` allows; an explicit capacity below this limit
is kept and logs a warning at start. See
[stream resource limits](architecture/stream-limits.md) for the rule and the sizing evidence.

Integration tests, `pgtest` and the module test fixtures are in
[database tests](database-tests.md).

`make generate` runs sqlc, pinned in `tools/go.mod`, against `db/migrations/`.
`sqlc.yaml` has one entry per module: `db/queries/identity/`,
`db/queries/realtime/`, `db/queries/org/`, `db/queries/conversation/` and
`db/queries/unread/` each
generate their store's `sqlcgen`. No query files sit directly in `db/queries/`.
Every entry sets `omit_unused_structs`, so a table no query uses gets no struct.
Commit the pgx/v5 output; CI rejects generation changes to committed files. Never edit
generated files.

`make check` runs tools tests uncached (`go -C tools test -race -count=1 ./...`).
Both use [`tools/internal/sqlwalk`](../tools/internal/sqlwalk/doc.go) for
`module.QueryName` query loading, unnamed SQL and duplicate rejection, `pg_query_go`
JSON parsing, statement/CTE/subquery traversal and the FROM `unnest` permission
rule. Organisation scope and table ownership policies stay in their respective
checkers. Shared reason validation
rejects pending approval and requires issue, PR-comment or numbered decision
provenance for maintainer claims (not approval verification). The parser needs
cgo and a C compiler (Xcode locally, GCC on CI).

Scopecheck requires each owned table's own WHERE scope in SELECT, UPDATE and DELETE,
including CTE bodies; joins never carry scope. UPDATE may not assign the scope
column (`organization_id`, or `id` for `organization`). Ownership comes from migration
columns, with `organization` scoped by `id` and an explicit installation-wide
list; unknown ownership, scoped list entries (except singleton `setup`) and stale
entries fail. Plain INSERT VALUES and INSERT SELECT with at most one source pass:
a same-statement CTE or unqualified FROM `unnest` of `sqlc.arg(name)::bigint[]`
or `sqlc.arg(name)::uuid[]` parameters, alone or mixed. The latter supplies
parameter-only candidate bounds for set-based range writes (#727) and
channel/topic IDs for counts (#740); aliases may name its output columns. It is
not a table and needs no scope, but each table joined to it in a SELECT or UPDATE
still needs its own WHERE scope.
CTEs containing it are checked normally. Tablecheck still rejects foreign writes.
Subqueries in VALUES, RETURNING or the INSERT SELECT remain unsupported, as do
joins (including comma joins) in that SELECT, physical-table INSERT SELECT reads,
ON CONFLICT and other INSERT shapes; full INSERT checking belongs in a follow-up.
CTE reads need no scope; outer joins, derived tables and set operations fail.
Other function relations, unnest arguments other than unqualified, unsized
`bigint[]` or `uuid[]` `sqlc.arg` parameters and unnest outside FROM fail, as do
LATERAL, ROWS FROM, WITH ORDINALITY and column type definitions (see [import checks](architecture/import-checks.md)).
`tools/scopecheck/allowlist.txt` uses `module.QueryName reason…`; stale, unnecessary
and `PENDING MAINTAINER:` entries fail (case-insensitive, with any non-alphanumeric
separator between the marker words). Migration statement/body ownership checks
and the two reviewed access exemptions are described with table ownership in
[import checks](architecture/import-checks.md).

## Development seed data

Filling a development database with synthetic conversations, and its
safety rules, is in [`seed-data.md`](seed-data.md).

## Generated schema reference

With the Compose database running and `RIBBITTO_DATABASE_URL` exported,
apply migrations and regenerate the [schema reference](schema/README.md):

```sh
make migrate
make schema-docs
```

tbls is pinned in its own module, `tools/tbls/go.mod`, to isolate its
dependencies from templ and sqlc. `make schema-docs` runs
`go tool -modfile=tools/tbls/go.mod tbls doc --rm-dist` from the repository
root.
`.tbls.yml` selects Markdown with Mermaid diagrams, without Graphviz or
`schema.json`. The command replaces all of `docs/schema/`, so keep
hand-written documentation elsewhere. Commit the
generated directory with schema changes. CI migrates a fresh PostgreSQL 18
database, regenerates the whole directory and requires
`git status --porcelain docs/schema` to be empty, including untracked files.
