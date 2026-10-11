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

Feed and topic writers return `(changed, error)` in the caller's transaction:
`changed` is true only when an actual unread message became read, and false on
error. Feed subtraction uses the returned neighbours under the same
organisation/member/channel lock, after establishing the implicit join prefix.
Only newly covered intervals enter conversation's transaction-bound, scoped
message-existence query, batched as one multirange; an empty difference skips
it. Topic detection reuses the locked unread candidates. Repeated, older,
empty, floor-only, join-prefix-only and identical-range writes report false.
The result describes uncommitted writes: a rollback discards them. Reading
synchronously calls its injected, context-free publisher with organisation and
member only when changed, after a successful transaction return or the one
exception below. A known rollback or commit failure publishes nothing;
cancellation immediately after commit cannot suppress publication. The commit,
and any rollback, run under their own five-second limit instead of the
request's cancellation and deadline (`platform.InTxDetachedCommit`; within the
ten-second shutdown grace), so request cancellation alone no longer turns a
commit in flight into a reported failure (Grok on #837), and a call to an unresponsive server returns within the limit;
pgx then closes that connection and the pool discards it in the background,
under pgx's own cleanup limits. A server's error or `ROLLBACK` reply, a COMMIT
never sent, and any failure while beginning or writing publish nothing. Only a
COMMIT sent without a reply within the limit (`platform.ErrCommitUnknown`) of a
read with `changed=true` publishes anyway, once, to the same organisation and
member — the exception to #828's commit-then-publish rule, approved by the
maintainer. That publication is a **recount request**, not a successful read:
the request still fails with the error, and the tabs only recount persisted
state, which is right whether or not the read committed. Limits: it may be
spurious (the read did not commit; a retried read may publish again), it may
reach tabs **before** the commit completes on the server, so their recount can
still miss it, and a process that dies between commit and publication publishes
nothing. Recovery in those cases is the recount every stream does on
(re)connect: option B has no bounded repair without reconnect (#711).
Production wires one `unread.Changes` registry to Reading and sidebar watches;
without a hub the publisher remains nil.
Concurrent feed/topic reads share the channel lock, including lock-row creation,
so the same unread message cannot cause two publications. Posting discards the
result and retains its existing
transaction order and durable event notification; branching keeps `Merge`'s
error-only entry point over the same locked union. Neither adapter calls the
reading publisher; both retain their durable `message.posted` notification.
`Merge` still delegates to `MergeCovered`: posting and branching also sort ranges
and build the difference. [Short matched measurements](read-advance-results.md)
compare real posting/branching transactions and fragmented unions with the
pre-coverage baseline (the non-blocking note on #828). Sorting/subtraction adds
allocations without extra SQL. #832's overnight posting/delivery comparison
follows separately; short measurements suffice for #711, not latency equivalence.

A newly read message
`m` adds `[p + 1, n)`, with `p` the channel's previous message (or 0) and
`n` its next (or `m + 1`), so every bounded gap contains an unread message.

- **Feed, cursor `S` (current):** `unread.FeedWriter` uses injected factories
  to read org's committed cursor and conversation's next message in the caller's
  transaction, then unions the prefix. One probe finds the first message above
  `S` (`n`, or `S + 1`), then `[0, n)`: it deletes every range it absorbs,
  so detection sorts returned neighbours for subtraction (`O(R log R)`),
  plus the batched existence probe, and stays `O(1)` in normal use.
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
  not only the loaded window. `conversation.Posting.PostFromPage` takes
  org's sequence first and refuses `S` above the pre-post committed limit.
  Its injected `PostReadWriterIn`, wired in `cmd/ribbitto`, binds the feed/topic
  write and own-message union to that transaction. Conversation supplies the
  predecessor and the scoped post/move guard; unread reads no message table.
  A post from a `?before=` page has no cursor: posting reads only the author's
  own message with `[p + 1, m + 1)` in the same transaction, leaving every other
  message's read state unchanged and raising no topic floor.

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
visibility triggers the first automatic POST and subsequent DOM applications
advance it while visible. Hidden applications wait until visibility returns.
htmx sends one reading POST at a time, then after success the newest shown cursor.
Failure also sends a strictly newer shown cursor; resending the same cursor
waits for the next visibility or application trigger. The same writers union
reordered requests from several tabs without losing read state.

A cursor above the organisation's committed `event_seq` is refused, so any
later move has a higher sequence than the floor it raises.
