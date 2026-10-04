# Database development

**Keep it current:** update this file when configuration, the migration
workflow or the integration-test setup changes.

`RIBBITTO_DATABASE_URL` configures both the server and the explicit
`migrate up|down|status` command. The server checks connectivity but never
applies migrations. `.env.example` contains the local Compose URLs; export
them into the shell as shown in the README. `.env` is ignored by Git.

`make db-up` starts PostgreSQL 18 on loopback port 5432 and waits for its
health check. Its password is for development only. `make db-down` stops
it without deleting the named volume at `/var/lib/postgresql`.

`db/migrations` holds numbered goose SQL files embedded in the binary.
It is owned by `internal/platform/postgres`; only its packages,
`cmd/ribbitto`, three target-version tests in `internal/infra/postgres`
and `internal/org/internal/postgres/member_handle_test.go` (until step 5)
may import it. The runner uses goose and pgx's
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

Integration tests use `RIBBITTO_TEST_DATABASE_URL`, an admin connection to
the `postgres` database as the `postgres` superuser. `pgtest.New(t)` (now in
`platform`) creates
a database per test, cloned from a migrated template named by the first
12 hex characters of a SHA-256 hash over sorted migration names and contents.
A dedicated admin connection serializes template creation with an exclusive
session advisory lock (`pg_advisory_lock`). Unfinished builds are removed
before rebuilding; a transaction publishes the final name and makes the template
unconnectable. Every call checks the template under the lock without Go
global caching. Cleanup closes the pool before dropping the database with
`WITH (FORCE)`. `pgtest.NewEmpty(t)` clones `template0` for migration tests.
The `cmd/ribbitto` acceptance tests also start from `pgtest.NewEmpty`, apply
the platform's `Migrate` as the CLI does, and serve the shared production handler
on HTTPS with cookie jars. They verify the account flow, request protections,
organisation isolation and a ten-client setup race, and, over HTTP/2, event
streams end to end (`TestEventStream`, `TestM3Acceptance`,
`TestShutdownEndsOpenStreams`).
Do not point this variable at a production server.
Tests skip only when the variable is unset; an empty or broken value fails.
`RIBBITTO_REQUIRE_DB=1` also makes an unset URL fail, as required in CI.

```sh
RIBBITTO_REQUIRE_DB=1 go test -count=1 -v ./internal/infra/postgres/...
env -u RIBBITTO_TEST_DATABASE_URL -u RIBBITTO_REQUIRE_DB go test -count=1 -v ./internal/infra/postgres/...
```

Top-level PostgreSQL-backed tests in `internal/infra/postgres` and
`internal/web` run in parallel with isolated databases and test-local state.
Their subtests remain sequential, preserving shared fixtures, clocks and
order-dependent assertions.

`pgtest.OrganizationWithOwner(t, pool, slug, channelName)` creates an organisation
at `event_seq` 1, an owner account at `<slug>@example.org`, an owner membership
with handle `owner` and `joined_event_seq` 1, and exactly one default channel
with the supplied name. The organisation and account display names equal the
slug. It returns the organisation, account and member IDs plus the channel.

`pgtest.Organization`, `Account`, `Member` and `Channel` remain composable
building blocks for partial setups or fixtures with different values.
`Organization` sets the supplied `event_seq`; `Member` sets the supplied role,
handle and `joined_event_seq` without advancing it. `Account` uses a placeholder
password hash and creates no membership. `Channel` uses the store with an
explicit name/default flag and leaves sequences unchanged. All fixture helpers
use `t.Context()` internally; none creates a setup row. Schema, migration and
adversarial tests keep direct SQL to express states these helpers should not hide.

`make generate` runs sqlc, pinned in `tools/go.mod`, against `db/migrations/`.
`sqlc.yaml` has one entry per query set: the files directly in `db/queries/`
generate `internal/infra/postgres/sqlcgen/`, and each module's directory
(`db/queries/identity/`, `db/queries/realtime/`, `db/queries/org/`)
generates its store's `sqlcgen`. Every entry sets `omit_unused_structs`, so a table no query uses
gets no struct. Commit the pgx/v5
output; CI rejects generation changes to committed files. Never edit
generated files.

## Development seed data

Filling a development database with synthetic conversations, and its
safety rules, is in [`seed-data.md`](seed-data.md).

## Generated schema reference

With the Compose database running and `RIBBITTO_DATABASE_URL` exported,
apply migrations and regenerate the [schema reference](schema/README.md):

```sh
go run ./cmd/ribbitto migrate up
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
