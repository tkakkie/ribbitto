# Unread writes

Read-state writes are unions under a per-member, per-channel lock; the set
only grows ([rules](../domain/unread.md),
[decision 32](../decisions/32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md)).
`unread.Reading` owns feed/topic transactions; their writers use that transaction.
Posting and branching union their author's read state in their own transactions
and retain their durable `message.posted` notification, never the reading publisher.
The [range writer](../../internal/unread/internal/postgres/writer.go) and
[topic writer](../../internal/unread/internal/postgres/topic.go) implement the unions.

## Unknown-COMMIT exception

Reading publishes only after a successful commit with `changed=true`, except
when that read's COMMIT was sent without a reply: it publishes one **recount
request** to the same organisation and member, while the request still fails.
This is not proof of a successful read; tabs recount persisted state. A known
rollback, a server error reply, a COMMIT never sent or a begin/write failure
publishes nothing ([publication rule](../../internal/unread/reading.go)).

The recount request may be spurious if the read did not commit; a retried read
may publish again. It may reach tabs **before** the commit completes, so their
recount can miss the eventual commit. If the process stops between commit and
publication, the notification is lost. Recovery is the recount on every stream
(re)connect; no bounded repair is guaranteed before reconnect.
