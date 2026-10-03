# Feature map

Each feature's packages and the tables it owns, and the known exceptions to
table ownership. [Packages](packages.md) lists every package and its allowed
imports.

**Keep it current:** update this file in the same pull request whenever a
feature gains or loses a package or a table, or an exception is added.

The code is layered today, and the direction is a modular monolith by
feature, migrated after M3 ([decision 14](../decisions/14-a-modular-monolith-by-feature-migrated-after-m3.md)).
Until then, new code goes into feature packages inside the layers, and each
feature logically owns tables: only that feature writes them, apart from
the known exceptions below. A feature may own no tables. Every
package-import edge is listed in [`docs/dependencies.md`](../dependencies.md).

| Feature | Packages and files | Owns |
|---|---|---|
| `identity`: accounts, passwords, sessions, signing in, sign-up | `identity` (module root, step 1a), `app/signup`; `infra/postgres` `account.go`, `session.go`, `signup.go`; `web` `signin.go`, `signup.go` | `account`, `session` |
| `org`: organisations, memberships, authorisation, first-run setup | `app/authz`, `app/member`, `app/setup`; `infra/postgres` `authz.go`, `member.go`, `setup.go`; `web` `org.go`, `setup.go` | `organization` (including `event_seq`, `event_log_boundary_seq`), `member`, `setup` |
| `channel`: public conversations | `app/channel`; `domain/channel.go`; `infra/postgres/channel.go`; `db/queries/channel.sql`; `web/channel.go` (channel handlers; the file also serves `message`), `web/view/channel.templ` | `channel` |
| `message`: plain-text posts and history | `app/message`; `domain/message.go`; `infra/postgres/message.go`, `message_reader.go`; `db/queries/message.sql`; `web/channel.go` (history, `?before=` paging, posting), `web/view/channel.templ`, `web/view/message.templ`, `web/static/message-*.js` | `message` |
| `topic`: conversations inside a channel, the default topic, branching *(decision 21)* | `app/topic`; `domain/topic.go`; `infra/postgres/topic.go`, `branch.go`; `db/queries/topic.sql`; `web/channel.go`, `web/view/channel.templ` (topic views and list), `web/branch.go`, `web/view/branch.templ`, `web/static/branch-selection-v1.js` | `topic` |
| `realtime` | `internal/realtime` *(M3)*; `domain/event.go`; `infra/postgres/event_log.go`, `event_reader.go`, `event_cleaner.go`; `db/queries/event_log.sql`; `web/stream.go` (the SSE endpoint), `web/stream_renderer.go` (live renderer and render cache), `web/stream_sender.go` (SSE sender); `web/static/message-stream-v*.js` (SSE glue, shared with `message`) | `event_log` |

The shared kernel, which any feature may use: the IDs and value types in
`internal/domain`, the per-organisation `event_seq` and `event_log_boundary_seq`, and the authorisation
entry point `app/authz`. Other files in `internal/web` (routing, forms,
middleware, views) and the composition roots serve every feature.

`ChannelStore` and `MessageStore` accept a pool or a caller-owned
transaction; `PostingStore` owns the posting transaction (sequence first,
then the message and event). Message references to channels and `org`'s members use
composite foreign keys including `organization_id`. History uses one
newest-first keyset query, `ListMessagesBefore`, with a nullable upper
sequence bound for the latest page, an optional scoped topic filter passed
explicitly through `Reader.Before` and `History`, and no author joins. `Reader.One` reads
one message by organisation, channel and `event_seq`, returning `ErrNotFound`
for a missing or out-of-scope message. The use cases
(`app/channel`, `app/message`) exist. `message.Reader` resolves authors through
org's exported `member.Directory.LookupMembers` (member IDs filtered by
organisation, returning handles and account IDs), then identity's
`identity.Directory.LookupDisplayNames` (only those account IDs). Their adapters
own the queries in `member.sql` and `account.sql`; message never queries
those tables. `MessageReader` shares one read-only repeatable-read transaction
across the channel and sidebar (through `channel.Service`), the selected topic
and the bounded topic list (through `topic.Store`), history, both author
lookups and the topic batch through `topic.Directory.LookupTopics`, plus the
shared-kernel `organization.event_seq` on the latest channel or topic page.
It returns `message.ChannelPage`; older pages have no event cursor.
`MessageReader.One` reads one message, its authors and topic in its own snapshot.
Live labels come from that shared load through the existing render cache, keyed
by organisation, channel, sequence and language, with no extra read per stream
per event. `MessageReader.Many` reads only a move's message IDs with the same snapshot
and directory batches; its shared render corrects feed labels and checkbox
sources and supplies topic-page removals and ordered insertions.
Malformed topic paging links use a scoped topic lookup without history;
topic posts rely on the lookup inside the posting transaction.

**Known exceptions.** Cross-feature writes that must commit atomically:

- setup (`org`) writes `organization`, `account`, `member`, `channel` and
  `setup`, so it creates `identity`'s first `account` and the `channel`
  feature's default channel (a completed setup must never lack one);
- creating a channel (`channel`) writes its default `topic` in the same
  statement, so a channel never exists without one (decision 21, #307);
- branching (`topic`) advances `organization.event_seq`, moves messages by
  writing `message.topic_id` and posts its notice into `message`, in one
  transaction with both events (decision 21, #305);
- sign-up (`identity`) writes `account` and `member` and advances
  `organization.event_seq`, which belong to `org`;
- posting (`message`) advances `organization.event_seq` before inserting
  the message, because the sequence must be taken in the writing
  transaction ([decision 5](../decisions/05-one-event-sequence-per-organisation.md));
- these flows call realtime's transaction-bound `NewEventLog(tx)` writer
  (`AppendMessagePosted`, `AppendMemberJoined` or `AppendMessagesMoved`)
  immediately after the message, member or move, so `event_log` commits with
  the entity and its sequence (#156, #257, #305);
- realtime retention (#161) writes org's `event_log_boundary_seq`,
  because the boundary and events must be read in the same snapshot.

Their atomicity and `event_seq` ordering stay as they are. They are
resolved at migration, by an orchestrating module or a shared transaction.
A new exception needs its issue to say why, and is added to this list.

## Target

Where the migration goes ([decision 26](../decisions/26-modules-by-feature-layout-seams-and-order.md), with [decision 27](../decisions/27-channels-topics-and-messages-are-one-conversation-module.md)'s `conversation` module); the code above is today's.

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
| every package (`domain.ID` = `kernel.ID` alias; `identity`'s `domain` import for it) | `kernel` `ID` | 0 | 5 |
| `infra/postgres/pgtest` (delegates `New`, `NewEmpty`; keeps feature fixtures) | `platform` lifecycle helpers | 0 | 5 (fixtures move with their modules) |
| four `infra` target-version tests | `db/migrations` (temporary allowance) | 0 | their module's step, or 5 |
| `internal/infra/postgres` | the `Tx`/`Snapshot` bridge (temporary allowance) | 0 | 5 |
| account and session stores (`infra`) | `identity`'s store interfaces | 1 | 1b |
| `identity` | `domain` `Account`, `ValidateEmail` | 1 | 1a-2 |
| `app/setup`, `app/signup` | `identity.Hasher` | 1 | 3 |
| `app/message`; page snapshot, `One`, `Many` (`infra`) | `identity` accounts | 1 | 4 (the use case replaces the caller) |
| setup, sign-up (`infra`) | `identity` account writes | 3 (until then, a `legacy_account.sql` copy of `CreateAccount` and `GetAccountByID` on the `infra` sqlc entry, from step 1) | 3 |
| posting, setup, sign-up, branching (`infra`) | `realtime` event appends | 2 | each flow's own step: 3, 4 |
| `realtime` reader and retention | `org` sequence bounds, boundary write | 2 | 3 |
| posting, branching, page snapshot, `One`, `Many`, page cursor (`infra`) | `org` sequence, members, cursor | 3 | 4 |
| setup (`org`) | `conversation` default channel | 3 | 4 |

Inside `conversation`, a channel's default topic, branching's moves and
notice, and the channel and topic reads of posting and the page snapshot
are direct calls in one transaction or snapshot (decision 27).
