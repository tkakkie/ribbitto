# Modules: the migration target

Where the migration goes ([decision 26](../decisions/26-modules-by-feature-layout-seams-and-order.md), with [decision 27](../decisions/27-channels-topics-and-messages-are-one-conversation-module.md)'s `conversation` module); [the feature map](features.md) describes today's code.

**Construction.** Each module consists of three parts:
- `internal/<module>`: types, errors, use cases and consumer interfaces;
- `internal/<module>/internal/postgres`: its store and `sqlcgen`;
- `internal/<module>/<module>pg`: wiring.

`<module>pg`'s constructors take the pool and the use cases' other dependencies (a clock, the shared hasher, a canceller) and return the use cases. Its `Tx`- or `Snapshot`-taking factories implement other modules' consumer interfaces, whose result types the consumer declares. A wiring package may import its own module's root and store, and the roots of modules whose interfaces it implements. It may not import other wiring or stores; only `cmd/*` and tests import it. Roots and stores follow the table's import column.

A cross-module flow's root use case owns its transaction through an injected runner (`org.TxRunner` for setup and sign-up, `conversation.TxRunner` for posting and branching), which the wiring implements over the pool with `platform.InTx`. The use case passes the runner's `Tx` to the factories it was given; stores bind to the `Tx` they are given and never open, commit or roll back. A use case reading several modules owns its snapshot the same way, through an injected runner (`conversation.SnapshotRunner` for the page snapshot, `One` and `Many`) over `platform.InSnapshot`, and passes its `Snapshot` to its factories.

`internal/kernel` holds `ID`. `internal/platform/postgres` holds the pool, migrations, lifecycle test helpers, and the opaque `Tx` and `Snapshot` with their open, commit and rollback operations. Its bridge package (handle to pgx) may be imported only by stores. Feature fixtures stay with their module's tests.

| Module | Owns | May import (roots) | Step |
|---|---|---|---|
| `identity`: accounts, passwords, sessions, sign-in | `account`, `session` | — | 1 |
| `realtime`: event log, retention, hub, stream loop, envelope | `event_log` | — | 2 |
| `org`: organisations, members, authorisation (`Membership`), setup, sign-up | `organization`, `member`, `setup` | `identity`, `realtime` | 3 |
| `conversation`: channels, topics, branching, posting, history, the page snapshot use case | `channel`, `topic`, `message` | `identity`, `org`, `realtime` | 4 |

`internal/web` stays the UI shell and imports module roots. Its per-kind
stream renderers are adapters for the payloads that `conversation`
registers with `realtime`. Step 0 creates `kernel` and `platform`; step 4
removes `internal/app`; step 5 removes `internal/domain` and
`internal/infra/postgres`.

**Known exceptions and temporary paths.** Every flow keeps its transaction
or snapshot. Each operation it needs from another module is one of the
injected factories described under *Construction*. The table below gives
the step that introduces the injected interface and the step that removes
the temporary implementation behind it.

| Flow or caller | Needs from | Interface from step | Temporary implementation until step |
|---|---|---|---|
| every package but `identity`, `realtime` and `conversation` (`domain.ID` = `kernel.ID` alias) | `kernel` `ID` | 0 | 5 |
| `infra/postgres/pgtest` (delegates `New`, `NewEmpty`; keeps org's and identity's fixtures plus a raw-SQL channel and default topic) | `platform` lifecycle helpers | 0 | 5 (fixtures move with their modules) |
| `conversation`'s topic backfill test, `realtime`'s event-log migration test (`internal/realtime/internal/postgres/event_log_test.go`), `org`'s handle upgrade test and `conversation`'s default-channel backfill test | `db/migrations` (temporary allowance) | 0 | 5 |

Setup's and sign-up's database tests live in `internal/org/orgpg`,
with local raw-SQL fixtures and event-log assertions.
Sign-up's setup organisation comes from `org`'s setup.

Inside `conversation`, a channel's default topic, branching's moves and
notice, and the channel and topic reads of posting and the page snapshot
are direct calls in one transaction or snapshot (decision 27).

The topic stream's and topic paging links' scope check is decision 26's
resolver in conversation's root: `conversation.Topics.Get` takes the
resolved `org.Membership`, so web never supplies the organisation.

Conversation's schema, constraint, payload-shape and backfill tests live in its
store with local raw-SQL fixtures. Its posting, branching and reader flow
tests live in `conversationpg`; `internal/infra/postgres` has no tests.
