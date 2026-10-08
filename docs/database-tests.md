# Database tests

**Keep it current:** update this file when the integration-test setup or
the module test fixtures change. Configuration, migrations and generated
code are in [database development](database.md).

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
