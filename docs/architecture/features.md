# Feature map

Each feature's packages and the tables it owns, and the known exceptions to
table ownership. [Packages](packages.md) lists every package and its allowed
imports.

**Keep it current:** update this file in the same pull request whenever a
feature gains or loses a package or a table, or an exception is added.

The code is layered today, and the direction is a modular monolith by
feature, migrated after M3 ([decision 14](../decisions/14-a-modular-monolith-by-feature-migrated-after-m3.md)).
New code goes in its module if migrated, else a feature package in the
layers. Each feature logically owns tables: only that feature writes them,
apart from the known exceptions below. A feature may own no tables. Every
package-import edge is listed in [`docs/dependencies.md`](../dependencies.md).

| Feature | Packages and files | Owns |
|---|---|---|
| `identity`: accounts, passwords, sessions, signing in | the `identity` module (root owns the email, password and display-name rules; store, `identitypg`; `db/queries/identity/`); `web` `signin.go` | `account`, `session` |
| `org`: organisations, memberships, authorisation, first-run setup, sign-up | the completed `org` module: `internal/org` (name/slug/handle rules and handle changes, the author directory, `member.joined`, event sequence and cursor/retention bounds; setup and sign-up each own their transaction), its store `internal/org/internal/postgres` and wiring `orgpg`; `db/queries/org/`; `web` `org.go`, `setup.go`, `signup.go` | `organization` (including `event_seq`, `event_log_boundary_seq`), `member`, `setup` |
| `channel`: public conversations | `conversation` (Channel, its name rule, errors and default name; Channels and ChannelStore); `conversationpg` (NewChannels); `conversation/internal/postgres/channel.go`, `db/queries/conversation/channel.sql`; `infra/postgres/channel.go`, `db/queries/channel.sql` (legacy copy until 4.15); `web/channel.go` (channel handlers; the file also serves `message`), `web/view/channel.templ` | `channel` |
| `message`: plain-text posts and history | `app/message`; `conversation/posted.go` (`message.posted`); `conversation/message.go` (type, body rule and errors); `infra/postgres/message.go`, `message_reader.go`; `db/queries/message.sql`; `web/channel.go` (history, `?before=` paging, posting), `web/view/channel.templ`, `web/view/message.templ`, `web/static/message-*.js` | `message` |
| `topic`: conversations inside a channel, the default topic, branching *(decision 21)* | `app/topic` (Store, Directory and branching); `conversation/moved.go` (`messages.moved`); `conversation/topic.go`, `topic_errors.go` (type, name rule and errors), `topics.go` (`Topics.Get`, the membership-scoped lookup); `conversationpg` (`NewTopics`); `conversation/internal/postgres/topic.go`, `db/queries/conversation/topic.sql` (`GetTopic`, legacy copy until 4.16); `infra/postgres/topic.go`, `branch.go`; `db/queries/topic.sql`; `web/channel.go`, `web/view/channel.templ` (topic views and list), `web/branch.go`, `web/view/branch.templ`, `web/static/branch-selection-v1.js` | `topic` |
| `realtime` | `internal/realtime` *(M3)*, its store `internal/realtime/internal/postgres` and wiring `realtimepg`; `db/queries/realtime/`; `infra/postgres/event_appender.go` (the flows' appender interface); `web/stream.go` (the SSE endpoint), `web/stream_renderer.go` (live renderer and render cache), `web/stream_sender.go` (SSE sender); `web/static/message-stream-v*.js` (SSE glue, shared with `message`) | `event_log` |

The shared kernel, which any feature may use: `internal/kernel` (`ID`) and
its alias in `internal/domain` until step 5 removes it. The
per-organisation `event_seq` and `event_log_boundary_seq` and the
authorisation entry point `org.Authorizer` are `org`'s ([decision 26](../decisions/26-modules-by-feature-layout-seams-and-order.md)); other
features use them only through the flows listed below and `org.Authorizer`.
Other files in `internal/web` (routing, forms, middleware, views) and the
composition roots serve every feature.

Conversation's channel store and infra's frozen `ChannelStore` accept a pool
or a caller-owned transaction, as does infra's `MessageStore`; `PostingStore` owns the posting transaction (sequence first,
then the message and event). It takes org's sequence through infra's
`EventSequenceIn` factory (`orgpg.SequenceIn`, adapted by a one-line
closure) on its own transaction; an unknown organisation is
`org.ErrNotFound`, which posting returns unchanged. Message references to channels and `org`'s members use
composite foreign keys including `organization_id`. History uses one
newest-first keyset query, `ListMessagesBefore`, with a nullable upper
sequence bound for the latest page, an optional scoped topic filter passed
explicitly through `Reader.Before` and `History`, and no author joins. `Reader.One` reads
one message by organisation, channel and `event_seq`, returning `conversation.ErrMessageNotFound`
for a missing or out-of-scope message. The use cases
(`conversation.Channels`, `app/message`) exist. `message.Reader` resolves authors through
org's exported `org.Directory.LookupMembers` (member IDs filtered by
organisation, returning handles and account IDs), then identity's
`identity.Directory.LookupDisplayNames` (only those account IDs). Their adapters
own the queries in `org/member.sql` and `identity/account.sql`; message never
queries those tables. `MessageReader` receives both directories through
conversation's `MemberDirectoryIn` and `AccountDirectoryIn` factories, adapted
from `orgpg.MembersIn` and `identitypg.AccountsIn` by one-line closures, and
the latest page's cursor through `EventCursorIn` (`orgpg.EventCursorIn`), all
bound to its snapshot. It
shares one read-only repeatable-read transaction
across the channel and sidebar (through `conversation.Channels`), the selected topic
and the bounded topic list (through `topic.Store`), history, both author
lookups and the topic batch through `topic.Directory.LookupTopics`, plus the
org's `organization.event_seq` on the latest channel or topic page.
It returns `message.ChannelPage`; older pages have no event cursor.
`MessageReader.One` reads one message, its authors and topic in its own snapshot.
Live labels come from that shared load through the existing render cache, keyed
by organisation, channel, sequence and language, with no extra read per stream
per event. `MessageReader.Many` reads only a move's message IDs with the same snapshot
and directory batches; its shared render corrects feed labels and checkbox
sources and supplies topic-page removals and ordered insertions.
Malformed topic paging links and the topic stream check the topic through
`conversation.Topics.Get`, scoped by the resolved membership, without history;
topic posts rely on the lookup inside the posting transaction.

Identity's store creates accounts in the caller's transaction through
`identitypg.AccountCreatorIn`. Closures in `cmd/*` and the tests adapt it to
org's factory and prove that the creator fits `org.AccountCreator`;
identity owns `ErrEmailTaken` and `ErrInvalidEmail`, which org maps to its
conflicts or field errors.

Org's test-only `GetOrganizationBySlug` and `GetMemberByOrganizationAndAccount`
queries live in `db/queries/org/`; conversation's schema test still uses
infra's `pgtest` fixtures until conversation moves in step 4.

**Known exceptions.** Cross-feature writes that must commit atomically:

- setup (`org.Setup`, in one transaction through `TxRunner`) writes `organization`, `account`, `member`, `channel` and
  `setup`, so it creates `identity`'s first `account` and the `channel`
  feature's default channel (a completed setup must never lack one) through
  the injected `AccountCreatorIn` and `DefaultChannelCreatorIn`
  (conversation's store), and its event through
  `EventAppenderIn`;
- creating a channel (`channel`) writes its default `topic` in the same
  statement, so a channel never exists without one (decision 21, #307);
- branching (`topic`) advances `organization.event_seq`, through org's
  injected sequence (`EventSequenceIn`) as posting does, moves messages by
  writing `message.topic_id` and posts its notice into `message`, in one
  transaction with both events (decision 21, #305);
- sign-up (`org.SignUp`) owns one transaction through `TxRunner`: org's
  registration writes take the sequence and create its member, with identity's
  account and realtime's event injected through `AccountCreatorIn` and
  `EventAppenderIn`, bound to the same transaction;
- posting (`message`) advances `organization.event_seq` before inserting
  the message, because the sequence must be taken in the writing
  transaction ([decision 5](../decisions/05-one-event-sequence-per-organisation.md));
- these flows call realtime's transaction-bound writer, an `EventAppender`
  that their store or root use case is given (realtime's, through
  `realtimepg.AppenderIn`),
  appending the payload their publisher's codec encoded
  immediately after the message, member or move, so `event_log` commits with
  the entity and its sequence (#156, #257, #305);
- realtime retention (#161) raises org's `event_log_boundary_seq`, under
  org's organisation lock, in the transaction that deletes the events,
  through `orgpg.RetentionBoundaryIn`, which `org`'s store implements.

Their atomicity and `event_seq` ordering stay as they are. They are
resolved at migration, by an orchestrating module or a shared transaction.
A new exception needs its issue to say why, and is added to this list.

## Target

Where the migration goes, the module construction rules and every temporary path until its step: [`modules.md`](modules.md).
