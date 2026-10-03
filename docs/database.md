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
It is owned by `internal/platform/postgres`; only that package and
`cmd/ribbitto` may import it. The runner uses a goose provider and pgx's
`database/sql` adapter without global goose configuration. The first
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
organisation at a time in transactions of at most 1,000 expired rows, selected
in sequence order using the existing `(organization_id, seq)` index. Each transaction locks only that
organisation before deleting events and advancing its replay boundary, then
commits before the next batch. Errors and timeouts keep committed progress;
the next tick retries remaining work. The listing uses an organisation-scoped
`EXISTS` scan on the same index; no additional index or migration is needed.
Messages and unread inputs are never deleted.

Integration tests use `RIBBITTO_TEST_DATABASE_URL`, an admin connection to
the `postgres` database as the `postgres` superuser. `pgtest.New(t)`
(`internal/platform/postgres/pgtest`) creates
a database per test, cloned from a migrated template named by the first
12 hex characters of a SHA-256 hash over sorted migration names and contents.
A dedicated admin connection serializes template creation with an exclusive
session advisory lock (`pg_advisory_lock`). Unfinished building databases
are removed before
rebuilding; a transaction publishes the final name and makes the template
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

`make generate` runs sqlc, pinned in `tools/go.mod`, against `db/migrations/`
and `db/queries/`. Commit its pgx/v5 output in `internal/infra/postgres/sqlcgen/`;
CI rejects generation changes to committed files. Never edit generated files.

## Development seed data

`cmd/seed` requires an empty, disposable development database. Before
connecting, it refuses `RIBBITTO_DATABASE_URL` hosts (including fallbacks)
other than `localhost`, `127.0.0.1` or `::1`. Loopback can still reach a
local production database or a remote tunnel; use only a disposable one.
With `RIBBITTO_DATABASE_URL` exported:

```sh
go run ./cmd/ribbitto migrate up
go run ./cmd/seed -messages 20000
```

`-messages N` is the positive number of scripted messages **per channel**
before topic fixtures (default 100). The command creates Paper Lantern
Studio (`paper-lantern`), five fictional members and four channels including
`general`. Each run prints a fresh random password shared by its members:
sign in as `mira@example.test` (owner), or another script handle at
`example.test`. Nothing fixed or published signs in.

Development mode then adds `rooftop-garden` and `garden-time` in `general`.
`topic.Brancher.Branch` moves one extra script message into each, leaving
notices and durable `messages.moved` events for replay.
`message.Service.PostToTopic` adds `message.PageSize + 10` (60) more posts
to `rooftop-garden`: 64 extra messages including notices, also with
`-messages 1`. The original N posts remain in each default topic.

After seeding, run `make dev`, sign in at `http://localhost:8080/` and open
`general` to see the notices and topic labels. Select `rooftop-garden`
from the sidebar to see **Load older** (61 messages; 11 on the older page),
or `garden-time` for one branched message. Topic URLs are
`/organizations/paper-lantern/channels/<channelID>/topics/<topicID>`;
IDs vary per run.

For load tests on a disposable machine, add `-streams 300
-streams-per-account 16 -sessions-per-account 2 -output /tmp/loadtest.json`
(on one command line). The cap defaults to 16, matching the production per-account stream cap (#160);
this flag only sizes fixtures and changes no production caps or defaults.
The command creates `max(5, ceil(streams / cap))` accounts in the same
organisation, including the five fictional members: 300 / 16 needs 19,
so it adds 14. Sessions per account default to 1; multiple sessions still
share that account's stream cap. Without load-test flags, no sessions or
credential file are created. Load-test mode adds no named topics or topic
fixtures; each channel still gets its default topic. All runs limit total
messages (including development fixtures and notices) plus accounts ×
sessions to 100,000, checked without overflow before writes. With four
channels, development mode therefore accepts at most `-messages 24984`.

The named JSON file is created exclusively with mode `0600`; existing files
and paths inside Git repositories (including worktrees) are refused before
database writes. Its format is
`{"organization_slug":"paper-lantern","channel_ids":["<UUID>"],"accounts":[{"handle":"mira","tokens":["<token>"]}]}`.
All channels and accounts are included. Tokens are fresh sessions with normal
30-day expiry; only their hashes reach PostgreSQL. Plain tokens go only to
this file, never to stdout or logs. Keep it outside repositories.

Loopback alone does not prove a database is disposable. These credentials
work against **any server using the seeded database**: keeping the database
and every server using it on the disposable machine is an operating
requirement. After a run (successful or interrupted), stop those servers,
drop the disposable database and delete the credential file, which may be
empty or incomplete on failure. Start again with a fresh migrated database;
deleting the file alone does not invalidate sessions.

`cmd/seed/conversations.json` contains English and Japanese exchanges,
Unicode names and layout edge cases. Have a person review scripts before
committing. Exchanges repeat in file order until N posts, possibly ending
partway through. Identical flags reproduce members, channels, bodies,
authors and order; IDs, timestamps and hashes vary. Existing use cases
post messages now, without backdating.

Completed setup makes the command refuse all writes, including on rerun.
Each use case commits separately: after an interrupted seed, discard the
disposable database and start with a fresh migrated one. Seeding enables
sign-up only inside this command; it does not change the server's settings.

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
