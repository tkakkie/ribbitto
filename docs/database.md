# Database development

**Keep it current:** update this file when configuration, the migration
workflow or the integration-test setup changes.

`RIBBITTO_DATABASE_URL` configures both the server and the explicit
`migrate up|down|status` command. The server checks connectivity but never
applies migrations. `.env.example` contains the local Compose URLs; export
them into the shell as shown in the README. `.env` is ignored by Git.

`make ai-env` creates/migrates a path-hashed dev database through the admin
`RIBBITTO_TEST_DATABASE_URL`. Its ignored `.env.local` stores only its name and
app/metrics ports. `make dev`, `make migrate DIRECTION=up|down|status`,
`make seed ARGS='-messages 100'` and `make schema-docs` read it. For other
commands: `python3 scripts/ai_env.py run <command>`. Without `.env.local`,
shell configuration applies. The test admin URL stays unchanged.

Automatic ports use 20000–32767, below the usual OS ephemeral ranges,
starting at a path-derived candidate; `ai-env.json` in the shared Git
directory is published atomically under the OS advisory lock `ai-env.lock`.
Allocation skips reserved/bound IPv4/IPv6 ports; `AI_APP_PORT`/`AI_METRICS_PORT`
overrides use the same checks. Reruns keep reservations with a running server.
Stale entries absent from Git's worktrees or missing `.git` metadata release
ports and drop their path-derived dev databases. Only registry entries are
considered; cleanup never scans by database prefix or touches test templates.
`make ai-env-clean` drops only this worktree's dev database and releases ports.
`make ai-health` distinguishes missing/invalid admin configuration, connection
failures and SQL failures without credentials;
`make check` runs it before tests when DB configuration is set or required.
`make check-ai-env` tests this tooling.

Run `python3 scripts/ai_capacity.py WT1 WT2 PACKAGES PARALLEL` outside the
sandbox, with this change in both worktrees and `bin/ai-db` built. It reports
two uncached required-DB checks' exit codes and counts of lines matching
`53300|too many clients`, sampled peak/initial client backends (including one
persistent observer connected before checks), server limit, `-p`, `-parallel`
and pool limits. Each check runs in its own process group, killed on cleanup.
Output stays in a 0600 `bin/ai-capacity-*.log` in each worktree; logs may contain
credentials, so do not share them. The report never prints URLs. The runner
reuses the inherited/default module cache and GOPATH. SQL pools are unbounded
(0); pgx
defaults to max(4, CPUs), unless the admin URL overrides it. Choose settings
with headroom, then repeat to validate them; settings await these runs.

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
`WITH (FORCE)`. `pgtest.NewEmpty(t)` clones `template0` for migration tests,
and `pgtest.NewMigrator(t, pool)` moves it to a version with `UpTo`, `Up`,
`Down` and `DownTo`, failing the test on any error.
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
RIBBITTO_REQUIRE_DB=1 go test -count=1 -v ./internal/conversation/...
env -u RIBBITTO_TEST_DATABASE_URL -u RIBBITTO_REQUIRE_DB go test -count=1 -v ./internal/conversation/...
```

Top-level PostgreSQL-backed tests in conversation's store, `conversationpg`
and `internal/web` run in parallel with isolated databases and test-local state.
Their subtests remain sequential, preserving shared fixtures, clocks and
order-dependent assertions, unless each subtest opens its own database.

Fixtures live with the module that owns their tables
([decision 29](decisions/29-test-fixtures-live-with-the-module-that-owns-their-tables.md)):
`identitytest.Account`, `orgtest.Organization` and `orgtest.Member`, and
`conversationtest.Channel` and `conversationtest.Topic`. Only tests and higher
fixture packages import them; they import no store, wiring or bridge.

`conversationtest.OrganizationWithOwner(t, pool, slug, channelName)` creates an organisation
at `event_seq` 1, an owner account at `<slug>@example.org`, an owner membership
with handle `owner` and `joined_event_seq` 1, and exactly one default channel
with the supplied name. The organisation and account display names equal the
slug. It writes org's and identity's rows only through `orgtest` and
`identitytest`, and returns the organisation, account and member IDs plus a
`ChannelFixture` with the same fields as `conversation.Channel`.
`TestOrganizationWithOwner` pins these premises.

The other five remain composable building blocks for partial setups or
fixtures with different values.
`Organization` sets the supplied `event_seq`; `Member` sets the supplied role,
handle and `joined_event_seq` without advancing it. `Account` uses a placeholder
password hash and creates no membership. `Channel` inserts the channel and its
default topic in one raw-SQL CTE with an explicit name/default flag, satisfying
the deferred foreign key, and leaves sequences unchanged. `Topic` inserts one
named, non-default topic in the supplied organisation and channel, returns a
`TopicFixture` with its ID, organisation, channel, name and creation time, and
leaves sequences and messages unchanged (`TestTopic` pins these premises).
All fixture helpers use `t.Context()` internally; none creates an event or setup row. Schema, migration and
adversarial tests keep direct SQL to express states these helpers should not hide.

`make generate` runs sqlc, pinned in `tools/go.mod`, against `db/migrations/`.
`sqlc.yaml` has one entry per module: `db/queries/identity/`,
`db/queries/realtime/`, `db/queries/org/` and `db/queries/conversation/` each
generate their store's `sqlcgen`. No query files sit directly in `db/queries/`.
Every entry sets `omit_unused_structs`, so a table no query uses gets no struct.
Commit the pgx/v5 output; CI rejects generation changes to committed files. Never edit
generated files.

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
