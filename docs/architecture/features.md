# Feature map

Each feature's packages and the tables it owns, and the known exceptions to
table ownership. [Packages](packages.md) lists every package and its allowed
imports.

**Keep it current:** update this file in the same pull request whenever a
feature gains or loses a package or a table, or an exception is added.

The code is a modular monolith by feature ([decision 14](../decisions/14-a-modular-monolith-by-feature-migrated-after-m3.md)):
`identity`, `realtime`, `org` and `conversation` are modules
([modules](modules.md)).
New code goes in its module. Each feature logically owns tables: only that feature's
SQL writes them. A feature may own no tables. Every
package-import edge is listed in [`docs/dependencies.md`](../dependencies.md).

| Feature | Packages and files | Owns |
|---|---|---|
| `identity`: accounts, passwords, sessions, signing in | the `identity` module (root owns the email, password and display-name rules; store, `identitypg`, test fixtures `identitytest`; `db/queries/identity/`); `web` `signin.go` | `account`, `session` |
| `org`: organisations, memberships, authorisation, first-run setup, sign-up | the `org` module: `internal/org` (name/slug/handle rules and handle changes, the author directory, `member.joined`, event sequence and cursor/retention bounds; setup and sign-up each own their transaction), its store `internal/org/internal/postgres`, wiring `orgpg` and test fixtures `orgtest`; `db/queries/org/`; `web` `org.go`, `setup.go`, `signup.go` | `organization` (including `event_seq`, `event_log_boundary_seq`), `member`, `setup` |
| `conversation`: channels, topics, branching, posting, history and the page snapshot *(decisions 21, 27)* | the `conversation` module: `internal/conversation` (channel, topic and message types, rules and errors; `Channels`, `Topics` (the membership-scoped lookup), `Posting`, `Brancher` and `Reader` (the page snapshot, `One`, `Many`); `message.posted` and `messages.moved`; posting and branching own their transaction, `Reader` its snapshot), its store `internal/conversation/internal/postgres`, wiring `conversationpg` and test fixtures `conversationtest`; `db/queries/conversation/`; `web` `channel.go` (channel and topic pages, history, `?before=` paging, posting), `branch.go`, `view/channel.templ`, `view/message.templ`, `view/branch.templ`, `web/static/message-*.js`, `web/static/branch-selection-v1.js` | `channel`, `topic`, `message` |
| `realtime` | `internal/realtime` *(M3)*, its store `internal/realtime/internal/postgres` and wiring `realtimepg`; `db/queries/realtime/`; `internal/web/stream.go` (the SSE endpoint), `internal/web/stream_renderer.go` (live renderer and render cache), `internal/web/stream_sender.go` (SSE sender); `web/static/message-stream-v*.js` (SSE glue, shared with `conversation`) | `event_log` |

The shared kernel, which any feature may use: `internal/kernel` (`ID`). The
per-organisation `event_seq` and `event_log_boundary_seq` and the
authorisation entry point `org.Authorizer` are `org`'s ([decision 26](../decisions/26-modules-by-feature-layout-seams-and-order.md)); other
features use them only through the flows listed below and `org.Authorizer`.
Other files in `internal/web` (routing, forms, middleware, views) and the
composition roots serve every feature.

How features reach each other's data (stores, posting's transaction, history
and the page snapshot) is in
[cross-feature access](cross-feature-access.md).

Org's test-only `GetOrganizationBySlug` and `GetMemberByOrganizationAndAccount`
queries live in `db/queries/org/`. [Modules](modules.md) says where
conversation's tests live. Another feature's tests write a table only through
its owner's test-only fixture package; [modules](modules.md) states the
fixture rule.

**Known exceptions.** Cross-feature writes that must commit atomically:

These flows use the owning modules' injected APIs, never direct foreign-table
SQL. The [module manifest](../../module_imports_test.go)'s `ownsTables` is checked
against every query by [`tools/tablecheck`](../../tools/tablecheck/table_test.go)
in `make check`. Foreign reads need a query/table exemption with a reason;
pending maintainer reasons and stale entries fail. Writes have no exemptions.

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
  transaction ([decision 5](../decisions/05-one-event-sequence-per-organisation.md));
- these flows call realtime's transaction-bound writer, an `EventAppender`
  that their root use case is given (realtime's, through
  `realtimepg.AppenderIn`),
  appending the payload their publisher's codec encoded
  immediately after the message, member or move, so `event_log` commits with
  the entity and its sequence (#156, #257, #305);
- realtime retention (#161) raises org's `event_log_boundary_seq`, under
  org's organisation lock, in the transaction that deletes the events,
  through `orgpg.RetentionBoundaryIn`, which `org`'s store implements.

Each flow commits in one transaction, to which the injected factories of
the other modules it writes are bound, so its atomicity and `event_seq`
ordering hold ([modules](modules.md)).
A new exception needs its issue to say why, and is added to this list.

## Module construction

How a module is built and what it may import: [`modules.md`](modules.md).
