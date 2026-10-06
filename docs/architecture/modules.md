# Modules: the migration target

Where the migration goes ([decision 26](../decisions/26-modules-by-feature-layout-seams-and-order.md), with [decision 27](../decisions/27-channels-topics-and-messages-are-one-conversation-module.md)'s `conversation` module); [the feature map](features.md) describes today's code.

**Construction.** Each module consists of three parts, and a module that owns tables may add an optional fourth ([decision 29](../decisions/29-test-fixtures-live-with-the-module-that-owns-their-tables.md)):
- `internal/<module>`: types, errors, use cases and consumer interfaces;
- `internal/<module>/internal/postgres`: its store and `sqlcgen`;
- `internal/<module>/<module>pg`: wiring;
- `internal/<module>/<module>test`: test-only raw-SQL fixtures for its own tables.

`<module>pg`'s constructors take the pool and the use cases' other dependencies (a clock, the shared hasher, a canceller) and return the use cases. Its `Tx`- or `Snapshot`-taking factories implement other modules' consumer interfaces. The consumer owns those interfaces and their factory types; their result types are the provider root's where it exports one (for example `org.DirectoryEntry`), never a duplicate (K7 on #502). A wiring package may import its own module's root and store, and the roots of modules whose interfaces it implements. It may not import other wiring or stores; only `cmd/*` and tests import it. Roots and stores follow the table's import column.

A cross-module flow's root use case owns its transaction through an injected runner (`org.TxRunner` for setup and sign-up, `conversation.TxRunner` for posting and branching), which the wiring implements over the pool with `platform.InTx`. The use case passes the runner's `Tx` to the factories it was given; stores bind to the `Tx` they are given and never open, commit or roll back. A use case reading several modules owns its snapshot the same way, through an injected runner (`conversation.SnapshotRunner` for the page snapshot, `One` and `Many`) over `platform.InSnapshot`, and passes its `Snapshot` to its factories.

`internal/kernel` holds `ID`. `internal/platform/postgres` holds the pool, migrations, lifecycle test helpers, and the opaque `Tx` and `Snapshot` with their open, commit and rollback operations. Its bridge package (handle to pgx) may be imported only by stores. Fixtures that other packages' tests need live in the owning module's fixture package (`identitytest`, `orgtest`, `conversationtest`), which only tests and higher fixture packages import; scenario-specific SQL stays in the test (decision 29).

| Module | Owns | May import (roots) | Step |
|---|---|---|---|
| `identity`: accounts, passwords, sessions, sign-in | `account`, `session` | — | 1 |
| `realtime`: event log, retention, hub, stream loop, envelope | `event_log` | — | 2 |
| `org`: organisations, members, authorisation (`Membership`), setup, sign-up | `organization`, `member`, `setup` | `identity`, `realtime` | 3 |
| `conversation`: channels, topics, branching, posting, history, the page snapshot use case | `channel`, `topic`, `message` | `identity`, `org`, `realtime` | 4 |

`internal/web` stays the UI shell and imports module roots. Its per-kind
stream renderers are adapters for the payloads that `conversation`
registers with `realtime`. Steps 0 to 4 are done: step 0 created `kernel`
and `platform`, steps 1 to 4 moved the modules, and step 4 removed
`internal/app`. Step 5 removes `internal/domain` and
`internal/infra/postgres`.

**Known exceptions and temporary paths.** Every flow keeps its transaction
or snapshot. Each operation it needs from another module is one of the
injected factories described under *Construction*. The table below gives
the step that introduces the injected interface and the step that removes
the temporary implementation behind it.

| Flow or caller | Needs from | Interface from step | Temporary implementation until step |
|---|---|---|---|
| `org`, `web`, `infra/postgres/pgtest`, `cmd/seed` and some tests (`domain.ID` = `kernel.ID` alias) | `kernel` `ID` | 0 | 5 |
| `infra/postgres/pgtest` (delegates `New`, `NewEmpty`; keeps a copy of the fixtures in `identitytest`, `orgtest` and `conversationtest` until its callers switch in 5.5–5.9) | `platform` lifecycle helpers | 0 | 5 (5.11 deletes it) |

Setup's and sign-up's database tests live in `internal/org/orgpg`,
with local raw-SQL fixtures and event-log assertions.
Sign-up's setup organisation comes from `org`'s setup.

Inside `conversation`, a channel's default topic, branching's moves and
notice, and the channel and topic reads of posting and the page snapshot
are direct calls in one transaction or snapshot (decision 27). Creating a
channel (`conversation.Channels.Create`) writes its default topic in the
same statement, so a channel never exists without one (decision 21, #307).

The topic stream's and topic paging links' scope check is decision 26's
resolver in conversation's root: `conversation.Topics.Get` takes the
resolved `org.Membership`, so web never supplies the organisation.

Conversation's schema, constraint, payload-shape and backfill tests live in its
store. Its store tests take ordinary rows from the owners' fixture packages
(decision 29), keeping raw SQL for invalid and historical states. Its posting,
branching and reader flow tests live in `conversationpg`;
`internal/infra/postgres` has no tests.
