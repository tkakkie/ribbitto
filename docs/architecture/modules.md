# Modules: the migration target

Where the migration goes ([decision 26](../decisions/26-modules-by-feature-layout-seams-and-order.md), with [decision 27](../decisions/27-channels-topics-and-messages-are-one-conversation-module.md)'s `conversation` module); [the feature map](features.md) describes today's code.

**Construction.** Each module consists of three parts:
- `internal/<module>`: types, errors, use cases and consumer interfaces;
- `internal/<module>/internal/postgres`: its store and `sqlcgen`;
- `internal/<module>/<module>pg`: wiring.

`<module>pg`'s constructors take the pool and the use cases' other dependencies (a clock, the shared hasher, a canceller) and return the use cases. Its `Tx`- or `Snapshot`-taking factories implement other modules' consumer interfaces, whose result types the consumer declares. A wiring package may import its own module's root and store, and the roots of modules whose interfaces it implements. It may not import other wiring or stores; only `cmd/*` and tests import it. Roots and stores follow the table's import column.

`internal/kernel` holds `ID`. `internal/platform/postgres` holds the pool, migrations, lifecycle test helpers, and the opaque `Tx` and `Snapshot` with their open, commit and rollback operations. Its bridge package (handle to pgx) may be imported only by stores. Feature fixtures stay with their module's tests.

| Module | Owns | May import (roots) | Step |
|---|---|---|---|
| `identity`: accounts, passwords, sessions, sign-in | `account`, `session` | — | 1 |
| `realtime`: event log, retention, hub, stream loop, envelope | `event_log` | — | 2 |
| `org`: organisations, members, authorisation (`Membership`), setup, sign-up | `organization`, `member`, `setup` | `identity`, `realtime` | 3 |
| `conversation`: channels, topics, branching, posting, history, the page snapshot use case | `channel`, `topic`, `message` | `identity`, `org`, `realtime` | 4 |

`internal/web` stays the UI shell and imports module roots. Its per-kind
stream renderers are adapters for the payloads that `conversation`
registers with `realtime`. Step 0 creates `kernel` and `platform`; step 5
removes `internal/domain`, `internal/app` and `internal/infra/postgres`.

**Known exceptions and temporary paths.** Every flow keeps its transaction
or snapshot. Each operation it needs from another module is one of the
injected factories described under *Construction*. The table below gives
the step that introduces the injected interface and the step that removes
the temporary implementation behind it.

| Flow or caller | Needs from | Interface from step | Temporary implementation until step |
|---|---|---|---|
| every package but `identity` and `realtime` (`domain.ID` = `kernel.ID` alias) | `kernel` `ID` | 0 | 5 |
| `infra/postgres/pgtest` (delegates `New`, `NewEmpty`; keeps feature fixtures) | `platform` lifecycle helpers | 0 | 5 (fixtures move with their modules) |
| four `infra` target-version tests | `db/migrations` (temporary allowance) | 0 | their module's step, or 5 |
| `internal/infra/postgres` | the `Tx`/`Snapshot` bridge (temporary allowance) | 0 | 5 |
| `app/setup`, `app/signup` | `identity.Hasher`, `identity.Account` | 1 | 3 |
| `app/message`; page snapshot, `One`, `Many` (`infra` `MessageReader`, given `identitypg.AccountsIn`) | `identity` accounts | 1 | 4 (the use case replaces the caller) |
| setup, sign-up (`infra`) | `identity` account writes through injected `org.AccountCreatorIn` (`identitypg.AccountCreatorIn`, adapted by a closure) | 3.9 | sign-up 3.11, setup 3.12; until then `legacy_account.sql` copies `CreateAccount` and `GetAccountByID` on the `infra` sqlc entry |
| `infra/postgres` appender interface (`EventAppender`) and the kind list | `realtime` types (root import, temporary allowance) | 2 | 5 |
| the kind registry (`postgres.EventKinds()`, used by `cmd/ribbitto` and tests) | each publisher's `realtime.Router` | 2 | 3 and 4, as each module registers its own |
| event payload codecs in `app/message` and `app/topic` | their kinds' payloads, owned by the publisher | 2 | 4 (`message.posted` and `messages.moved`, with `conversation`); `member.joined`'s moved into `org` in 3.4 |
| posting, setup, sign-up, branching (`infra`) | their transactions on `platform.InTx`, their queries through `pgxbridge.Tx` | 2 | each flow's own step: setup and sign-up 3, posting and branching 4 |
| posting, setup, sign-up, branching (`infra`) | `realtime` event appends, through the `EventAppenderIn` factory their stores take (`realtimepg.AppenderIn`, adapted by `cmd/*` and the tests) | 2 | each flow's own step: 3, 4 |
| the reader's tests (`event_reader_test.go`, `move_event_test.go` in `infra`, built through `realtimepg`) | — | 2 | a follow-up moves them into `realtime`'s store tests |
| posting, branching, page snapshot, `One`, `Many`, page cursor (`infra`) | `org` sequence, members (infra's `MemberDirectoryIn`, implemented by `orgpg.MembersIn` through a closure, since 3.7), cursor | 3 | 4 |
| setup (`org`) | `conversation` default channel | 3 | 4 |

`app/message.Reader` keeps its `org.Directory` field until step 4, when
conversation declares its own.

Inside `conversation`, a channel's default topic, branching's moves and
notice, and the channel and topic reads of posting and the page snapshot
are direct calls in one transaction or snapshot (decision 27).
