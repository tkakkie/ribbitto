# Cross-feature access

How features read and write each other's data today: which stores accept a
pool, a transaction or a snapshot, and how posting, history and the page
snapshot reach org's and identity's data. The [feature map](features.md)
lists each feature's packages and tables and the known exceptions; the
[architecture index](README.md) lists the other files.

Conversation's channel store accepts a pool or a caller-owned transaction.
Its `CreateChannel` creates a non-default channel; only setup's
`DefaultChannelCreator` supplies the default flag inside the store.
`conversation.Posting` owns the posting transaction (sequence first, topic
reads, then the message and event) through its runner. It takes org's sequence
through conversation's `EventSequenceIn` factory (`orgpg.SequenceIn`, adapted
by a one-line closure) on that transaction; an unknown organisation is
`org.ErrNotFound`, which posting returns unchanged. Message references to channels and `org`'s members use
composite foreign keys including `organization_id`. History uses one
newest-first keyset query, `ListMessagesBefore`, with a nullable upper
sequence bound for the latest page, an optional scoped topic filter passed
explicitly through `Reader.Page` and `ReadStore`, and no author joins. `Reader.One` reads
one message by organisation, channel and `event_seq`, returning `conversation.ErrMessageNotFound`
for a missing or out-of-scope message. `conversation.Reader` resolves authors through
its own `MemberDirectory.LookupMembers`, served by org (member IDs filtered by
organisation, returning handles and account IDs), then `AccountDirectory.LookupDisplayNames`,
served by identity (only those account IDs). Their adapters
own the queries in `org/member.sql` and `identity/account.sql`; conversation never
queries those tables. `conversation.Reader` receives both directories through
conversation's `MemberDirectoryIn` and `AccountDirectoryIn` factories, adapted
from `orgpg.MembersIn` and `identitypg.AccountsIn` by one-line closures, and
every history page's cursor through conversation's `EventCursorIn` (`orgpg.EventCursorIn`), all
bound to its snapshot through `SnapshotRunner`. It
shares one read-only repeatable-read transaction
across the channel and sidebar, the selected topic and the bounded topic list
(through its snapshot-bound `ReadStore`), history, both author
lookups and the topic batch through `conversation.ReadStore.LookupTopics`, plus the
org's `organization.event_seq` on every channel or topic page, including
`?before=` pages. It returns `conversation.ChannelPage` with that snapshot cursor;
older pages connect with only the sidebar interest.
`conversation.Reader.Members` validates the channel through conversation's scoped
`GetChannel`, then reads its sidebar, org's ID-ordered `Directory.ListMembers`
(100 members plus one lookahead), identity's display names in one batch, and
org's stream cursor in the same snapshot. All public channels include every
organisation member today. The org query reads only its own member table;
channel scope stays in conversation. A member-ID cursor only advances the
organisation-scoped page and grants no access.
`conversation.Reader.One` reads one message, its authors and topic in its own snapshot.
Live labels come from that shared load through the existing render cache, keyed
by organisation, channel, sequence and language, with no extra read per stream
per event. `conversation.Reader.Many` reads only a move's message IDs with the same snapshot
and directory batches; its shared render corrects feed labels and checkbox
sources and supplies topic-page removals and ordered insertions.
Malformed topic paging links and the organisation stream's topic filter check the topic through
`conversation.Topics.Get`, scoped by the resolved membership, without history;
topic posts rely on the lookup inside the posting transaction.

Unread's store binds to a caller-owned transaction through `unreadpg.WriterIn`.
It locks or creates `channel_read`, unions the supplied join prefix before
the new range, and reads/deletes only primary-key-bounded neighbours. A
savepoint around creation recovers a concurrent unique conflict; it never
completes the caller's transaction. The caller supplies org's persisted join
sequence; no message or foreign-table query runs in unread's store.
`unread.FeedWriter` takes consumer-owned transaction factories, injected by
`newFeedWriter` in `cmd/ribbitto`: `conversationpg.MessageSequencesIn` supplies
`FirstMessageAfter` through conversation's own `FirstChannelMessageAfter`
sqlc query (organisation, channel, strictly above `S`, lowest sequence), and
`orgpg.EventCursorInTx` binds org's existing cursor query to the same transaction.
The feed refuses `S` above that cursor, then merges `[0, n)` (`S + 1` without
a next message). Its HTTP caller and the topic-view write remain planned.

Identity's store creates accounts in the caller's transaction through
`identitypg.AccountCreatorIn`. Adapters in `cmd/*` and the tests adapt it to
org's factory and prove that the creator fits `org.AccountCreator`;
identity owns `ErrEmailTaken` and `ErrInvalidEmail`, which org maps to its
conflicts or field errors.
