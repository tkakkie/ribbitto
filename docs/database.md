# Database development

**Keep it current:** update this file when configuration or the migration
workflow changes; the integration-test setup is in
[database tests](database-tests.md).

`RIBBITTO_DATABASE_URL` configures both the server and the explicit
`migrate up|down|status` command. The server checks connectivity but never
applies migrations. `.env.example` contains the local Compose URLs; export
them into the shell as shown in the README. `.env` is ignored by Git.

See [worktree environments](worktree-env.md) for per-worktree dev databases, ports, health checks and cleanup.

## Recount capacity configuration

`serve` accepts `RIBBITTO_ASYNC_RECOUNTS=on|off`; unset or empty means off,
and any other value fails startup. Off ignores all seven budget inputs below,
including malformed stray values, and preserves the database URL's pool settings
and pgx defaults. On requires every input explicitly, with no defaults:

| Environment variable | Budget input |
|---|---|
| `RIBBITTO_POOL_MAX_CONNS` | C: pool maximum per process |
| `RIBBITTO_RECOUNT_WORKERS` | W: configured recount workers |
| `RIBBITTO_DB_MAX_CONNECTIONS` | M: PostgreSQL max_connections |
| `RIBBITTO_DB_RESERVED_CONNECTIONS` | R: reserved connections |
| `RIBBITTO_DB_EXTERNAL_CONNECTIONS` | E: external/operations connections |
| `RIBBITTO_DB_PROCESSES` | P: maximum simultaneous ribbitto processes |
| `RIBBITTO_DB_NORMAL_HEADROOM` | H: connections for normal work per process |

All inputs must be signed 32-bit integers: C, W, M and P positive; R, E and H
non-negative. Startup reports all violated input and budget rules together:
`B = floor((M-R-E)/P)`, `0 < C <= B`, `0 < W <= C-H`. Deployment inputs
are operator declarations, not discovered database settings. Include overlap
during deployments in P. The registration limit is the effective
`RIBBITTO_MAX_STREAMS`, with no separate setting.

With M=100, R=3, E=20, P=2 and H=10, B=38. C=36 and W=26 pass;
C=39 or W=27 fail. **36/26 is a validation configuration, not a production
default.** On applies C before opening the pool, overriding `pool_max_conns`
in the database URL. The platform's `PoolOptions.MaxConns=0` preserves existing
pool behavior. A startup `slog` record includes the effective pool maximum,
configured budget, stream registration limit and effective delivery mode;
no database URL or credentials are logged.

On constructs one scheduler with W workers and the process stream cap as its
registration limit, plus one delivery observer. Both share the production
stream composition and development metrics; shutdown joins the scheduler before
closing the shared pool. Off constructs neither and keeps synchronous delivery;
metrics report `scheduler.enabled: false` and `delivery.enabled: false`.
**Async recounts are not enabled by default and are not yet validated for
production.** Stage 2 / PR 3 of #865 decides activation using a freshness oracle
and mixed-load validation; opt-in wiring alone is not that validation.
See the [scheduler contract](architecture/recount-scheduler.md).

Member/account fairness is out of scope: FIFO is per stream, so sixteen tabs
receive more service than one tab. Equal account shares need an identity-aware
policy.

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
