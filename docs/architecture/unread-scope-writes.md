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
- **Feed run, cursor `S`, run `[F, T]` (dormant, #1486):** `ReadShown`
  validates `0 ≤ S < MaxInt64`, `S ≤` org's committed cursor and
  `1 ≤ F ≤ T ≤ S` before any write. Conversation's transaction-bound channel
  neighbours give `lo = prev(F) + 1` (or 0) and `hi = next(T)` (or `T + 1`).
  `MergeCovered([lo, hi))` retains the join prefix and channel lock; the same
  bounded existence probe checks only newly covered intervals. Bounds need not
  name messages: exactly messages in `[F, T]` become read, preserving the gap
  invariant. Reading uses the existing commit/publication rule; no HTTP caller
  exists. Cost is the whole-feed path plus one predecessor statement.
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
- **Topic run, cursor `S`, run `[F, T]` (dormant, #1486):** `TopicWriter.ReadShown`
  validates the topic-run bounds before preparation. The same transaction-bound
  candidate query adds `F ≤ event_seq ≤ T`, retaining `moved_event_seq ≤ S`.
  Under the channel lock, `TopicRunFloor` probes the prepared read set for the
  earliest unread message below `F`: its sequence above the floor, or its move
  sequence when moved in below the floor. `least(T + 1, blockers) - 1` gives
  the partial raise; subtraction happens in Go, and `Add` keeps the greater
  stored floor. With no blocker it reaches `T`, never passing unseen `(T, S]`;
  moves after `S` remain exceptions. Whole-topic reads keep floor `S` without
  this guard because they read everything through `S`. Reading retains the
  commit/publication rule. No HTTP caller exists; cost is today's topic path
  plus one guard statement.
- **Posting (current):** `conversation.Posting.Send` reads only the author's own
  message with `[p + 1, m + 1)`, leaving every other message's read state and
  topic floors unchanged. Conversation supplies the channel predecessor;
  `PostReadWriterIn`, wired through `compose.PostReads`, binds the own-message
  union and persisted join prefix to posting's transaction. Org is locked before
  `channel_read`; the union and event append commit or roll back together.
