# Unread writes

How a member's read state grows: the shared write under the channel lock and
each flow that calls it
([decision 32](../decisions/32-read-state-is-a-set-of-read-ranges-per-member-and-channel.md)).
Storage, the API between the modules and what the counts cost are in
[unread counts](unread-counts.md); the rules are in [unread](../domain/unread.md).
The per-scope writes are in [unread scope writes](unread-scope-writes.md).

Write ranges use `conversation.SequenceRange`. Unread owns `ChannelMessages`
and `TopicMessages`; conversation's wiring returns concrete transaction-bound
stores that satisfy them directly, without a range converter.

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
and build the difference. [Short matched measurements](../measurements/read-advance-results.md)
compare real posting/branching transactions and fragmented unions with the
pre-coverage baseline (the non-blocking note on #828). Sorting/subtraction adds
allocations without extra SQL. #832's overnight posting/delivery comparison
follows separately; short measurements suffice for #711, not latency equivalence.

The per-scope writes are in [unread scope writes](unread-scope-writes.md).

Read POSTs use an independent per-member admission budget after session and
membership resolution, before channel/topic lookups, parsing and Reading:
initial, unmeasured 4 requests/s, burst 16, at most four concurrent requests.
Refusals immediately return 429 with `Retry-After: 1`; admitted slots are
released on every exit, including panic and cancellation. Limits are in memory
per process ([retention](rate-limits.md#member-admission-for-reads)). PR #860's
client retries 429s until success or the page no longer needs the read. Some
of 16 simultaneous tabs are refused; under healthy service and subsiding
competing load, their read state converges without another user action.
Sustained overload has no success guarantee.

**HTTP caller (current):** `POST /organizations/{slug}/channels/{channelID}/read`
(and `/topics/{topicID}/read`) takes membership from URL authorization,
resolves the channel and topic through conversation, and calls `unread.Reading`
with `joined_event_seq` and body `cursor`: `from` with `through` calls
`FeedShown`/`TopicShown`; `scope=all` or cursor-only calls `Feed`/`Topic`.
Cursor-only is the bounded transition described in [reading](../domain/unread-reading.md).
Values remain body-only; repeated recognized keys across query and body fail.
Base-10 int64 parsing accepts leading zeros and either sign, but no spaces,
hex, empty values or overflow; unread validates the numeric bounds. Reading owns the transaction through
an injected runner; `unreadpg` binds the runner to the pool and the writers
to that transaction.
Cross-origin protection covers both routes and all forms. Missing cursor,
repeated keys, malformed integers, half runs, scope other than all, scope with
run bounds, invalid cursors and `before` or `around`
parameters are 400 without writes; invisible channels/topics and non-members
get 404. Errors roll back the transaction. htmx receives 204; plain forms
redirect to the same feed or topic. Latest pages carry their snapshot cursor;
visibility triggers the first automatic POST and subsequent DOM applications
advance it while visible. Hidden applications wait until visibility returns.
htmx 2.0.7 carries a focused submitter's `scope=all` on automatic requests
(source inference, not browser-verified here); it is equivalent today. S3a must
rewrite `scope` on every request before sending runs (amendment E).
htmx sends one reading POST at a time; newer applied cursors coalesce without
queuing old requests. A 429 retries until success or hide, form removal or
navigation, with one retry timer per page. Exponential backoff uses jitter and
an initial, unmeasured 8-second cap; `Retry-After` (seconds or HTTP date) is a
minimum even when longer. Success resets backoff and flushes newer shown work.
Other failures still flush only a strictly newer cursor; the same cursor waits
for another visibility or application trigger. Cleanup clears timers. With a
healthy network/server and competing read load subsiding, visible pages converge
without another user action, including after repeated refusals across 16 tabs;
sustained overload has no convergence guarantee. The same writers union
reordered requests from several tabs without losing read state.

A cursor above the organisation's committed `event_seq` is refused, so any
later move has a higher sequence than the floor it raises.
