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

`conversation.Posting.PostToTopic` validates and owns the transaction through
its `TxRunner`: its transaction-bound `EventSequence` takes the sequence, then
its `Writer` reads the default topic (and the selected topic, if supplied)
and inserts the message; `Post` is its shorthand for the default topic. It
encodes the post with `conversation.EncodePosted` and calls its
transaction-bound `EventAppender.Append` before commit.
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

