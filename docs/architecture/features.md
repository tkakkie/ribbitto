# Feature map

Each feature's packages and its table-ownership registry, and the known exceptions to
table ownership. [Packages](packages.md) lists every package and its allowed
imports.

**Keep it current:** update this file in the same pull request whenever a
feature gains or loses a package or a table, or an exception is added.

The code is a modular monolith by feature ([decision 14](../decisions/14-a-modular-monolith-by-feature-migrated-after-m3.md)):
`identity`, `realtime`, `org`, `conversation`, `presence`, `typing` and `unread` are modules
([modules](modules.md)).
New code goes in its module. Each feature logically owns tables: only that feature's
SQL writes them. A feature may own no tables. Every
package-import edge is listed in [`docs/dependencies.md`](../dependencies.md).

| Feature | Packages and files | Owns |
|---|---|---|
| `identity`: accounts, passwords, sessions, signing in | the `identity` module (root owns the email, password and display-name rules; store, `identitypg`, test fixtures `identitytest`; `db/queries/identity/`); `web` `signin.go` | `ownsTables` for `identity` in the [manifest](../../module_imports_test.go) |
| `org`: organisations, memberships, authorisation, first-run setup, sign-up | the `org` module: `internal/org` (name/slug/handle rules and handle changes, the author and paged member directory, `member.joined`, event sequence and cursor/retention bounds, the access epoch; setup and sign-up each own their transaction), its store `internal/org/internal/postgres`, wiring `orgpg` and test fixtures `orgtest`; `db/queries/org/`; `web` `org.go`, `setup.go`, `signup.go` | `ownsTables` for `org` in the [manifest](../../module_imports_test.go) |
| `conversation`: channels, topics, branching, posting, history, pins with authorized changes, durable invalidations, bounded snapshot reads and the overview's pins (#887–#889), and the page snapshot *(decisions 21, 27)*; records `message.moved_event_seq` and supplies transaction-bound channel and topic unread bounds and marks posts and branch notices read through an injected writer; and the snapshot-bound message queries `unread` counts with *(decision 32)* | the `conversation` module: `internal/conversation` (channel, topic and message types, rules and errors; `Channels`, `Topics` (the membership-scoped lookup), `Posting`, `Brancher`, `Pinning` and `Reader` (the page snapshot, the overview page with its pins, pins-only refreshes, `One`, `Many`, channel members, the sidebar snapshot that is also the channel list page); `message.posted`, `messages.moved` and `pins.changed`; channel creation, posting, branching and pinning own their transaction, `Reader` its snapshot), its store `internal/conversation/internal/postgres`, wiring `conversationpg` and test fixtures `conversationtest`; `db/queries/conversation/`; `web` `channel.go` (channel and topic pages, history, `?before=` paging, posting, channel creation), `channel_image.go` (image URLs and the overview's image change), `members.go` (the members and overview panels), `branch.go`, `pin.go` (pin and unpin), `pins_source.go` (shared live pins sections, #1205), `view/members.templ`, `view/panel.templ`, `view/pins.templ`, `view/channel.templ`, `view/channel_image.templ`, `view/shell.templ` (the app shell and the create-channel form), `view/message.templ`, `view/branch.templ`, `web/static/message-*.js`, `web/static/pins-sync-v1.js`, `web/static/branch-selection-v1.js`, `web/static/side-panel-v3.js`, `web/static/sidebar-scroll-v2.js` | `ownsTables` for `conversation` in the [manifest](../../module_imports_test.go) |
| `realtime` | `internal/realtime` *(M3)*, its store `internal/realtime/internal/postgres` and wiring `realtimepg`; `db/queries/realtime/`; `internal/web/stream.go` (the SSE endpoints and presence delivery), `internal/web/stream_renderer.go` (live renderer and render cache), `internal/web/stream_sender.go` (SSE sender); `web/static/message-stream-v*.js` (SSE glue, shared with `conversation`) | `ownsTables` for `realtime` in the [manifest](../../module_imports_test.go) |
| `presence` | `internal/presence` (memory-only online state, grace expiry, bounded snapshots and ordered changes/reset); `web` stream lifecycle seam, members-panel snapshot, shared indicator renders and owner adapter; `cmd/ribbitto` wiring (#768). | no tables |
| `typing` | `internal/typing`: memory-only channel/topic summaries, explicit starts/stops and generation publication, 5 s expiry, channel-stream lifetime, composer generations signal ingress, viewer-filtered delivery and stable feed/topic status regions ([contract](typing.md)). | no tables |
| `unread` *(decision 32)* | `internal/unread`, its store `internal/unread/internal/postgres`, wiring `unreadpg` and `db/queries/unread/`: feed and topic writes with injected conversation/org factories, transaction-bound locking, join prefix and range unions; `unread.Reading` runs them in its own transaction (an injected `TxRunner`) for `internal/web/read.go`; channel and topic counts through `conversation`'s snapshot-bound API ([unread counts](unread-counts.md)); counts bound to history pages and shown in the sidebar (`internal/web/sidebar.go`, `web/static/read-visibility-v6.js`, `web/static/sidebar-unread-v2.js`) | `ownsTables` for `unread` in the [manifest](../../module_imports_test.go) |

The shared kernel, which any feature may use: `internal/kernel` (`ID`). The
per-organisation `event_seq` and `event_log_boundary_seq` and the
authorisation entry point `org.Authorizer` are `org`'s ([decision 26](../decisions/26-modules-by-feature-layout-seams-and-order.md)); other
features use them only through the flows listed below and `org.Authorizer`.
Other files in `internal/web` (routing, forms, middleware, views) and the
composition roots serve every feature.

How features reach each other's data (stores, posting's transaction, history
and the page snapshot) is in
[cross-feature access](cross-feature-access.md).

Org's test-only lookup by slug lives in its store's tests; its test-only
`GetMemberByOrganizationAndAccount` query lives in `db/queries/org/`.
[Modules](modules.md) says where conversation's tests live. Another
feature's tests write a table only through its owner's test-only fixture
package; [modules](modules.md) states the fixture rule.

**Known exceptions.** Cross-feature writes that must commit atomically:

- setup (`org.Setup`, in one transaction through `TxRunner`) writes `organization`, `account`, `member`, `channel` and
  `setup`, so it creates `identity`'s first `account` and `conversation`'s
  default channel (a completed setup must never lack one) through
  the injected `AccountCreatorIn` and `DefaultChannelCreatorIn`
  (`conversationpg.DefaultChannelCreatorIn`), and its event through
  `EventAppenderIn`;
- branching (`conversation.Brancher`) owns one transaction through
  `TxRunner`, to which org's sequence (`EventSequenceIn`) and realtime's
  appender (`EventAppenderIn`) are bound, so both events take their
  sequence and commit with the move (decision 21, #305);
- sign-up (`org.SignUp`) owns one transaction through `TxRunner`: org's
  registration writes take the sequence and create its member, with identity's
  account and realtime's event injected through `AccountCreatorIn` and
  `EventAppenderIn`, bound to the same transaction;
- posting (`conversation.Posting`) advances `organization.event_seq` before inserting
  the message, because the sequence must be taken in the writing
  transaction ([decision 5](../decisions/05-one-event-sequence-per-organisation.md)); page posts also
  write unread's ranges and topic floor through injected `PostReadWriterIn` so
  the page scope and author's own message become read atomically (#723);
- pinning (`conversation.Pinning`) uses existing `EventSequenceIn` (org lock first)
  and `EventAppenderIn` to commit metadata with `pins.changed`, then raises the hub;
  channel access/limit precede writes; failures/no-ops roll back; read state stays untouched (#888);
- these flows call realtime's transaction-bound writer, an `EventAppender`
  that their root use case is given (realtime's, through
  `realtimepg.AppenderIn`),
  appending the payload their publisher's codec encoded
  immediately after the message, member or move, so `event_log` commits with
  the entity and its sequence (#156, #257, #305);
- realtime retention (#161) raises org's `event_log_boundary_seq`, under
  org's organisation lock, in the transaction that deletes the events,
  through `orgpg.RetentionBoundaryIn`, which `org`'s store implements.

These flows use the owning modules' injected APIs, never direct foreign-table
SQL. The [module manifest](../../module_imports_test.go)'s `ownsTables` is the
authoritative table registry; the table above points there rather than duplicating
it. [`tools/tablecheck`](../../tools/tablecheck/table_test.go) checks it against
migration-created tables and every production query in `make check`. Query foreign
reads need a query/table exemption with a reviewed reason; unused entries and
reasons containing "pending maintainer" (case-insensitive, with any
non-alphanumeric separator) fail.
Reasons mentioning the maintainer need an issue, PR comment or decision reference.
Query writes have no exemptions. The separate [migration gate](import-checks.md)
checks backfills and trigger routines, with reviewed access exemptions.

Each flow commits in one transaction, to which the injected factories of
the other modules it writes are bound, so its atomicity and `event_seq`
ordering hold ([modules](modules.md)).
A new exception needs its issue to say why, and is added to this list.

## Module construction

How a module is built and what it may import: [`modules.md`](modules.md).
