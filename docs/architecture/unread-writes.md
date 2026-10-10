# Unread writes

How a member's read state grows: the shared write under the channel lock and
each flow that calls it
([decision 32](../decisions/32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md)).
Storage, the API between the modules and what the counts cost are in
[unread counts](unread-counts.md); the rules are in [unread](../domain/unread.md).

The current store locks `channel_read` (inserting it if missing), then unions
the join prefix before the new range. Concurrent writes queue and commute; none
removes a read message. A savepoint recovers concurrent row creation. A
primary-key predecessor lookup bounds deletion to overlapping or touching
rows up to the new upper end, then inserts their union. The caller's
transaction commits or rolls back all writes.

A newly read message
`m` adds `[p + 1, n)`, with `p` the channel's previous message (or 0) and
`n` its next (or `m + 1`), so every bounded gap contains an unread message.

- **Feed, cursor `S` (current):** `unread.FeedWriter` uses injected factories
  to read org's committed cursor and conversation's next message in the caller's
  transaction, then unions the prefix. One probe finds the first message above
  `S` (`n`, or `S + 1`), then `[0, n)`: it deletes every range it absorbs,
  so its cost is proportional to the ranges merged, `O(R)` after heavy
  fragmentation and `O(1)` in normal use.
- **Branch notice (current):** conversation's organisation- and channel-scoped
  predecessor query returns `p` (or 0). An injected `unreadpg.WriterIn` adapter
  calls `Writer.Merge` with `[p + 1, m + 1)` and the persisted join prefix after
  inserting notice `m`, before its append. The organisation lock keeps `m`
  newest; all writes share branching's transaction. Moves leave read state unchanged.
- **Topic view, cursor `S` (current):** `unread.TopicWriter` validates org's
  committed cursor, then runs locked preparation, conversation's candidate
  query and the batch union with floor update, in the caller's transaction.
  Preparation establishes the join prefix and loads ranges and the floor;
  `conversation` returns unread bounds `[p + 1, n)` through `S`, excluding
  moves after `S`. Go coalesces the bounds; fixed statements merge only
  overlapping or touching ranges and raise the floor, retaining the lock.
  Cost: `O(R)` for the read set and the candidate rows; 87–88 ms for 9,999
  messages over 10,000 ranges in the benchmark.
- **Posting (planned)** with the composer's cursor `S`: the same write as the page's
  scope up to `S`, then a range for the new message; from a topic view the
  floor also rises to the new message when no message of the topic has an
  `event_seq` or `moved_event_seq` strictly between `S` and it. `S` is the
  newest durable sequence the page has applied and shown, never a sequence
  only received or the post's own response, so posting never reads a
  message the member has not seen.

**HTTP caller (current):** `POST /organizations/{slug}/channels/{channelID}/read`
(and `/topics/{topicID}/read`) takes membership from URL authorization,
resolves the channel and topic through conversation, and calls `unread.Reading`
with `joined_event_seq` and form `cursor`. Reading owns the transaction through
an injected runner; `unreadpg` binds the runner to the pool and the writers
to that transaction.
Cross-origin protection covers both routes. Invalid cursors and `?before=`
requests are 400 without writes; invisible channels/topics and non-members
get 404. Errors roll back the transaction. htmx receives 204; plain forms
redirect to the same feed or topic. Latest pages carry their snapshot cursor;
visibility triggers the first automatic POST, with no later advancement (#710).

A cursor above the organisation's committed `event_seq` is refused, so any
later move has a higher sequence than the floor it raises.
