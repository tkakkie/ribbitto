# Database development

**Keep it current:** update this file when configuration or the migration
workflow changes; the integration-test setup is in
[database tests](database-tests.md).

`RIBBITTO_DATABASE_URL` configures both the server and the explicit
`migrate up|down|status` command. The server checks connectivity but never
applies migrations. `.env.example` contains the local Compose URLs; export
them into the shell as shown in the README. `.env` is ignored by Git.

See [worktree environments](worktree-env.md) for per-worktree dev databases, ports, health checks and cleanup.

## Recount capacity

See [recount capacity](recount-capacity.md) for the shared computation limit
and its sizing from the effective pgx pool maximum.

## Database workflow

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

Migration 00013 adds nullable `channel.image` (text), an illustration key.
Existing rows stay NULL; web picks a stable image from the channel ID for
NULL or unknown keys. Down drops the column.

Migration 00014 adds nullable `message.pinned_by_member_id` and `pinned_at`.
A CHECK pairs their nullability; the pinner references `(organization_id, id)`
in `member`. A partial index on `(organization_id, channel_id, pinned_at DESC, id)`
covers pinned rows only. Pins retain no body copy. Down removes both columns
and the index.

Migration 00015 adds nullable `message.idempotency_key` (uuid) and the partial
unique index `message_idempotency_key_idx` on `(organization_id, member_id,
idempotency_key) WHERE idempotency_key IS NOT NULL` (#1155). Existing rows and
notices stay NULL. The index scans `message` and blocks writes during upgrade,
acceptable at current sizes; `CONCURRENTLY` would need goose's `NO TRANSACTION`.
Keys live as long as their message. Down drops the index and column.

Migration 00016 adds `message.format` (text, not null, default `plain_text`)
with the CHECK `message_format_check` allowing `plain_text` and `chat_v1`
([decision 39](decisions/39-message-bodies-use-a-small-frozen-markup.md)).
Existing rows become `plain_text` without a table rewrite; the CHECK scans
`message` during upgrade. Stored formats never change: posting and notices
insert `chat_v1` (#1203), so the default covers only older rows.
Down drops the column.

Migration 00017 adds `account.send_key` (text, not null, default `enter`)
with the CHECK `account_send_key_check` allowing `enter` and
`modifier_enter` (#1175). Existing accounts become `enter` through the
default without a table rewrite, so they keep sending on Enter; checking the
CHECK scans `account` while `migrate up` runs. Down drops the column.

Migration 00018 adds a message's quote (#1176,
[decision 38](decisions/38-a-quote-is-a-snapshot-owned-by-the-quoting-message.md)):
nullable `quoted_message_id`, `quoted_member_id` and `quote_text` (under the
body CHECKs). `message_quote_check` pairs author/text and requires both with
an ID. `UNIQUE (organization_id, id)` supports `message_quoted_message_fkey`
with `ON DELETE SET NULL (quoted_message_id)`; `message_quoted_member_fkey`
references `member`. The unique and partial `message_quoted_message_idx`
indexes build under the upgrade's table lock. Down drops them and the columns.

Migration 00019 adds nullable `message.typed_body_proof` (bytea): a 4-byte
big-endian byte length followed by SHA-256 of the normalized typed body.
A CHECK requires NULL or 36 bytes. No default or backfill: legacy rows stay
NULL. Keyed posts store it atomically with body/key; unkeyed posts and notices
store NULL. Down drops it. See [retry rules](architecture/posting.md).

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

`RIBBITTO_MAX_CHANNELS` caps channels per organisation, including the default.
Unset or empty selects `500`; any other value must contain only decimal digits,
be positive and fit int32 (at most 2,147,483,647), or startup fails. Lowering
it below the current count blocks new creation without changing existing data.
See [channel creation limits](domain/channels.md#creation-limits).

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

Two tools-module tests check every production query: tablecheck (ownership
and expressions) and scopecheck (organisation scope). Their rules are in
[query gate](architecture/query-gate.md).
Migration statement/body ownership checks
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

tbls is pinned in `tools/tbls/go.mod`, isolating its dependencies from templ
and sqlc. `make schema-docs` runs
`go tool -modfile=tools/tbls/go.mod tbls doc --rm-dist` at the repository root.
`.tbls.yml` selects Markdown/Mermaid, without Graphviz or `schema.json`.
It replaces `docs/schema/`; keep hand-written docs elsewhere and
commit the generated directory with schema changes. CI migrates a fresh
PostgreSQL 18 database, regenerates it and requires
`git status --porcelain docs/schema` to be empty, including untracked files.
