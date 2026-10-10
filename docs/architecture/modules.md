# Modules

How a module is built ([decision 26](../decisions/26-modules-by-feature-layout-seams-and-order.md), with [decision 27](../decisions/27-channels-topics-and-messages-are-one-conversation-module.md)'s `conversation` module), the modules, what they own and what they may import; [the feature map](features.md) maps features to their code.

**Construction.** A persistent module consists of three parts, and may add an optional fourth ([decision 29](../decisions/29-test-fixtures-live-with-the-module-that-owns-their-tables.md)):
- `internal/<module>`: types, errors, use cases and consumer interfaces;
- `internal/<module>/internal/postgres`: its store and `sqlcgen`;
- `internal/<module>/<module>pg`: wiring;
- `internal/<module>/<module>test`: test-only raw-SQL fixtures for its own tables.

A memory-only module such as `internal/presence` or `internal/typing` has just its root, constructed
directly in `cmd/ribbitto`; it needs no store, wiring or table fixtures.

A wiring package exports its use-case constructors, the `Tx`- and `Snapshot`-bound factories that other modules' consumers take, and `EventKinds`. Its runners and its own module's bindings stay unexported. `orgpg.NewHandleChanger` is a use-case constructor awaiting its handler. Store tests bind through `platform.InTx`/`InSnapshot` and the store's exported bindings; tests of the wiring's runners live in the wiring package. The wiring package's own external tests (`orgpg_test`, `conversationpg_test`) reach its runner through `NewTxRunnerForTest` in its `export_test.go`; no other package can.

`<module>pg`'s constructors take the pool and the use cases' other dependencies (a clock, the shared hasher, a canceller) and return the use cases. Its `Tx`- or `Snapshot`-taking factories implement other modules' consumer interfaces. The consumer owns those interfaces and their factory types; their result types are the provider root's where it exports one (for example `org.DirectoryEntry`), never a duplicate (K7 on #502). Where a provider's factory returns its own store type (`identitypg.AccountCreatorIn`, `orgpg.SequenceIn`, `realtimepg.AppenderIn`, `conversationpg.DefaultChannelCreatorIn`), a closure in the composition root adapts it to the consumer's factory type. A wiring package may import its own module's root and store, and the roots of modules whose interfaces it implements. It may not import other wiring or stores; only `cmd/*` and tests import it. Roots and stores follow the [module manifest](../../module_imports_test.go).

A cross-module flow's root use case owns its transaction through an injected runner (`org.TxRunner` for setup and sign-up, `conversation.TxRunner` for posting and branching), which the wiring implements over the pool with `platform.InTx`. The use case passes the runner's `Tx` to the factories it was given; stores bind to the `Tx` they are given and never open, commit or roll back. A use case reading several modules owns its snapshot the same way, through an injected runner (`conversation.SnapshotRunner` for the page snapshot, `One` and `Many`) over `platform.InSnapshot`, and passes its `Snapshot` to its factories.

`internal/kernel` holds `ID`. `internal/platform/postgres` holds the pool, migrations, lifecycle test helpers, and the opaque `Tx` and `Snapshot` with their open, commit and rollback operations. Its bridge package (handle to pgx) may be imported only by stores. Fixtures that other packages' tests need live in the owning module's fixture package (`identitytest`, `orgtest`, `conversationtest`), which only tests and higher fixture packages (`conversationtest` → `orgtest` → `identitytest`) import, never production code; it imports no store, wiring or bridge, and scenario-specific SQL stays in the test (decision 29). [Import checks](import-checks.md) says how this is enforced.

| Module | Owns |
|---|---|
| `identity`: accounts, passwords, sessions, sign-in | `account`, `session` |
| `realtime`: event log, retention, hub, stream loop, envelope | `event_log` |
| `org`: organisations, members, authorisation (`Membership`), setup, sign-up | `organization`, `member`, `setup` |
| `conversation`: channels, topics, branching, posting, history, the page snapshot use case | `channel`, `topic`, `message` |
| `presence`: per-process online state, grace expiry and ordered changes/reset; web rendering/delivery current (#768) | no tables |
| `typing`: per-process distinct-member channel/topic summaries and generation publication ([contract](typing.md)); expiry, ingress and delivery follow | no tables |
| `unread`: feed and topic writes, transaction-bound lock, join prefix, read-range unions and topic floors | `channel_read`, `read_range`, `topic_read_floor` |

`internal/web` stays the UI shell and imports module roots. Its per-kind
stream renderers are adapters for the payloads that `conversation`
registers with `realtime`.

Every flow keeps its transaction or snapshot. Each operation it needs from
another module is one of the injected factories described under
*Construction*. A temporary exception to these rules names the step or
issue that removes it (decision 26).

Setup's and sign-up's database tests live in `internal/org/orgpg`,
with the owners' fixture packages (decision 29) and local event-log
assertions.
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
branching and reader flow tests live in `conversationpg`.
