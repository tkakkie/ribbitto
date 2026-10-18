# Posting a message

Durable events and the event log they are written to: [real time](realtime.md#durable-event-log).

```mermaid
sequenceDiagram
  participant A as conversation.Posting
  participant DB as PostgreSQL
  participant H as realtime hub
  A->>DB: BEGIN
  A->>DB: UPDATE organization SET event_seq = event_seq + 1 WHERE id = $1 RETURNING event_seq
  A->>DB: INSERT message (event_seq = n)
  A->>DB: INSERT event_log (seq = n, event data)
  A->>DB: COMMIT
  A->>H: Notifier.Raise(organizationID, posted.EventSeq) (after commit)
```

`conversation.Posting.Send` validates and owns the transaction through
its `TxRunner` (`PostToTopic`, `PostFromPage` and `Post` delegate to it): its transaction-bound `EventSequence` takes the sequence, then
its `Writer` reads the default topic (and the selected topic, if supplied)
and inserts the message; `Post` is its shorthand for the default topic. It
encodes the post with `conversation.EncodePosted` and calls its
transaction-bound `EventAppender.Append` before commit. `PostFromPage` also
reads the composer's scope and own message through `PostReadWriterIn` in that
transaction, under org-first locking ([unread writes](unread-writes.md)).
Org's sequence (`orgpg.SequenceIn`) and realtime's appender
(`realtimepg.AppenderIn`) use that same transaction and sequence.
`conversation.NewPosting` accepts `conversation.Notifier`
(`Raise(organizationID kernel.ID, seq int64)`); a nil notifier disables
notifications. `Post` calls it only after the runner commits; `serve` wires
`realtime.Hub` to it. A channel outside the caller's organisation fails the
scoped topic lookup and rolls the sequence back with it. Composite foreign
keys also enforce the message's organisation scope.

- **Take the sequence number first.** The `UPDATE` (scoped to the
  organisation from the URL, `WHERE id = $1`) locks that organisation's row
  until commit, so sequence order equals commit order and no gap can be
  skipped by a reader. A rolled-back transaction also rolls back the
  increment: no holes. The value is needed for `message.event_seq`, hence
  first.
- **The cost** is that durable events of one organisation are serialised on
  that row. Keep these transactions short and always lock the organisation
  first.
- **One sequence, two uses:** the same value goes into `event_log.seq`
  (real-time ordering and replay) and `message.event_seq` (unread counts), so
  unread state still works after old `event_log` rows are deleted.
- **Only durable events go into `event_log`.** Typing indicators and presence
  are ephemeral and never replayed.
- **The hub is told the new sequence after commit.** It keeps, per
  organisation, the highest committed sequence it has seen — a *value*, not
  a one-shot signal (see [*No lost wakeups*](replay.md#ordering-and-replay)) — and only ever raises it.
  Events themselves are always read from `event_log`. A crash between
  commit and telling the hub loses nothing: readers catch up from the table.
  *Future work:* with more than one server, the value would travel as PostgreSQL `NOTIFY`
  (payload: organisation and sequence) sent inside the writing transaction;
  each listener would raise its local hub's value, and after reconnecting
  read `organization.event_seq` and raise the value to that (#236).

**Member references** ([decision 39](../decisions/39-message-bodies-use-a-small-frozen-markup.md#member-references)).
`Send` parses the validated body as `chat_v1` and, if it holds references,
looks them all up in one batch through org's directory
(`LookupMembers`, scoped to the membership's organisation) in a snapshot of
its own, before the transaction, so the organisation lock never waits on it.
A reference the directory does not return, missing or another
organisation's, is `ErrUnknownReference` (wrapping `ErrInvalidBody`): web
answers 422 with one message and keeps the draft. A member who leaves
between the check and the insert shows as the placeholder, like any later
leaver. `NewPagePosting` takes the directory; posting without one (seeding
and tests) refuses any reference as a server error. The store inserts
`format = 'chat_v1'` for posts and branching's notices; branching escapes
its notice (`EscapeChatV1`), so a topic name is never markup or a reference.

**Quotes** ([decision 38](../decisions/38-a-quote-is-a-snapshot-owned-by-the-quoting-message.md)).
`Send.Quote` names a message to quote whole. Before the transaction, `Send`
reads it through conversation's `ReadStore` (`GetMessages`, scoped to the
membership's organisation and the posting channel) in a snapshot of its own,
with the members a `chat_v1` original references, so the organisation lock
never waits on the directory. In the transaction, after the topic and under
the organisation lock that moves take too, `Writer.QuotedMessage` reads it
again with the same scope; only that read decides. The snapshot is the original's
own text, never its quote: `Entry.Text` with references as `@handle` or
`Send.UnknownMember`, trimmed by `ValidateMessageBody`, which refuses an
empty one as `ErrInvalidBody`. Missing, another organisation's and another
channel's originals are one `ErrQuoteNotFound` (web: 404), which rolls back
the sequence; so does an original deleted before the insert, through
`message_quoted_message_fkey`. A repeat compares the quote too.

**Repeated sends** ([decision 37](../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md#b-idempotency-key-1155)).
Web posts through `conversation.Posting.Send`, which takes the optional
cursor and the composer's `IdempotencyKey`; `PostToTopic` and `PostFromPage`
delegate to it. The insert stores the key and `typed_body_proof` atomically
with the body, and the partial unique index
`message_idempotency_key_idx` on (organisation, author, key) decides a repeat,
not a lookup first. Its `23505` becomes `ErrPostKeyTaken`, which rolls back
the transaction with its `event_seq` increment (no holes). `Send` then reads
the stored message by the membership's organisation, author and key in a
short transaction of its own (`Writer.KeyedMessage`) and returns it with no
event and no notification. The organisation lock serialises two sends with
one key, so the second always sees the first's row. A repeat compares the
whole normalized typed body by its stored proof
(4-byte big-endian byte length and SHA-256 digest). For legacy rows without a
proof, only an exact stored-body match with no member reference is equal
(`plain_text`, or `chat_v1` with none). A prefix is never a whole-body retry.
Typed and stored bodies are currently equal.
A repeat whose typed body, quote or channel differs returns
`ErrPostKeyReused` with the stored message:
it is not a retry, and its text must not be dropped. The topic is not
compared, because branching may have moved the message. `Sent.Topic` is the
requested place resolved before the insert; a repeat's composer and typing
stay there.

The channel page reads its channel, sidebar, history and both author batches
in one `REPEATABLE READ READ ONLY` transaction owned by `conversation.Reader`
through its `SnapshotRunner`, wired by `conversationpg.NewReader`.
Every history page also reads `organization.event_seq` in that snapshot. The
page renders it as `data-event-cursor` on `#organization-stream`, outside every
htmx swap, including `?before=` pages. Loading older history or replacing the
composer leaves the initial page cursor intact.
`conversation.Reader.One` uses the same snapshot pattern for an event's
organisation, channel and `event_seq`, returning the message with current
author names or `conversation.ErrMessageNotFound`.

