# Unread scope writes

The per-scope feed, branch-notice, topic-view and posting writes from [unread writes](unread-writes.md) are collected here.

A newly read message
`m` adds `[p + 1, n)`, with `p` the channel's previous message (or 0) and
`n` its next (or `m + 1`), so every bounded gap contains an unread message.

- **Feed, cursor `S` (current):** `unread.FeedWriter` uses injected factories
  to read org's committed cursor and conversation's next message in the caller's
  transaction, then unions the prefix. One probe finds the first message above
  `S` (`n`, or `S + 1`), then `[0, n)`: it deletes every range it absorbs,
  so detection sorts returned neighbours for subtraction (`O(R log R)`),
  plus the batched existence probe, and stays `O(1)` in normal use.
  Besides containment, the probe is bounded by the covered set's lowest and highest
  sequence, so it scans the channel's index from the first interval.
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
- **Posting (current)** with the composer's cursor `S`: the same write as the page's
  scope up to `S`, then a range for the new message; from a topic view the
  floor also rises to the new message when no message of the topic has an
  `event_seq` or `moved_event_seq` strictly between `S` and it. `S` is the
  newest durable sequence the page has applied, never a sequence only
  received or the post's own response, so posting never reads past the
  page's cursor; like every topic read, it covers the whole topic up to `S`,
  not only the loaded window. `conversation.Posting.Send` with a cursor takes
  org's sequence first and refuses `S` above the pre-post committed limit.
  Its injected `PostReadWriterIn`, wired through `compose.PostReads`, binds the feed/topic
  write and own-message union to that transaction. For a topic post with a
  cursor, `unread.PostReads` captures the page write's prepared topic after cursor
  validation and reuses it for the own-message `Add` and final floor update.
  Preparation runs once; both unions and the event append still commit or
  roll back together, with no prepared state surviving the transaction.
  Conversation supplies the
  predecessor and the scoped post/move guard; unread reads no message table.
  A post from a `?before=` page has no cursor: posting reads only the author's
  own message with `[p + 1, m + 1)` in the same transaction, leaving every other
  message's read state unchanged and raising no topic floor.
