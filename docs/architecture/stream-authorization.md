# Stream authorization cost

How a stream keeps #262's guarantee without one database query per
connection per event (#622). **Decided by the maintainer on 2026-10-08:
option (a2), with its triggers mandatory** ([Decision](#decision)). It is
implemented by #669 (epoch) and #670 (delivery); #671 measured the cost
([results](stream-authorization-results.md)). Current checks use fresh shared epochs and bounded cached allows,
reloading memberships independently on misses. Parent cancellation fails
closed. The comparison preserves the pre-#670 reasoning.

## Before #670

- **The check:** `Stream.deliver` (`internal/realtime/stream.go`) filters,
  renders, calls `Authorizer.MayReceive`, then sends.
  `org.Authorizer.MayReceive` (`internal/org/authorizer.go`) runs
  `GetMembershipBySlug` (`db/queries/org/member.sql`) every time, then checks
  the organisation and the audience in memory. A failed query stops the
  stream; it is never a deny.
- **The cost:** a post wakes every stream of its organisation; each
  caught-up stream that follows the post's channel (and topic) checks it
  once, while the others skip it before rendering
  (`internal/realtime/subscription.go`). So a post costs one query per
  interested stream, `N` below, plus shared work. In the benchmark and in
  #216 every stream followed one channel, so `N` was every open stream.
  With #227's caches the benchmark measured `N + 10` queries per post and passed 2,000 streams ([stream cost](stream-cost.md)).
  End to end at 10 posts/s with pgx's default pool of 10, 7,000 streams pass
  (7,015 queries per post) and 10,000 fail (p95 2.8 s over HTTP/1.1, 4.3 s
  through Caddy over HTTP/2); a 40-connection pool fails at 15,000
  ([load results](load-results.md)).
- **What revokes access:** nothing in the application yet. No code removes a
  member, changes a role, or renames or deletes an organisation, and every
  channel is public. Only direct SQL removes a membership, as #262's
  reproduction did. Private channels and member removal
  ([roadmap](../roadmap.md#after-the-mvp)) will add the real writers.
  Sign-out is separate: `Hub.CancelSession` ends the session's streams, with
  no query per event.

### #262's guarantee, testable

For an event E and a connection of account A: if a transaction that removes
A's access to E commits **before the check for E starts** — before E was
read, while it waited in a batch, or while its render waited on the
database — E is not sent and the cursor moves past it. A commit after the
check can still let that one event through (accepted on #262). Before #670, this
held because the check is a `READ COMMITTED` query whose snapshot is taken
after the render returns. `TestStreamRechecksAccessBeforeEachSend` and
`TestStreamDeniesAccessLostWhileRendering`
(`internal/realtime/stream_test.go`) pin it.

## Options

### (a) Membership kept until the organisation's access changes

Each (account, organisation) membership is cached with an organisation-wide
**access epoch**; a check allows from memory while the epoch is unchanged and
runs today's query when it changed. Whatever carries the signal, two rules
hold:
- every access-changing write bumps the epoch **in its own transaction**.
  Bumped after the commit, a check in between allows a revoked member; bumped
  in an earlier transaction, a reload in between caches the old membership
  under the new epoch;
- the membership and the epoch it is valid for are read in **one statement**,
  for the same reason.

The variants differ in how a check learns the current epoch.

**(a1) In-process signal.** The writer marks the organisation unsettled in
memory before committing and publishes the new epoch after commit or
rollback; while unsettled, checks query. No query per post. It holds only for
writes made by this process's code: direct SQL (today's only revocation
path), another process or an older binary is never seen, and the cache stays
stale until a restart. Across processes, LISTEN/NOTIFY arrives after the
commit and a dropped listener loses signals; polling bounds staleness to its
period. Both weaken "committed before the check" to "a while before".

**(a2) Durable epoch, read fresh and shared.** `organization.access_epoch`
is bumped by triggers on the tables that decide access (`member` deletes,
updates of role, organisation or account, and `TRUNCATE`; `organization` slug
updates; later the private-channel tables), so direct SQL and other binaries
bump it too. **The triggers are part of (a2):** bumps only in application
code are a weaker variant whose guarantee, like (a1)'s, covers only writes
through current code, though across processes.

A check takes the epoch from a read whose snapshot began after the check
started, and **an epoch value is never retained**: a stored or TTL-cached
epoch is stale. `realtime.Cache` gives this rule only with a `keep` that
rejects every value (`internal/realtime/cache.go`): callers that join a load
share a private second load started after it, and a caller arriving during
that one starts another. If shutdown ends that read, the cache returns the
parent's cause; `org` also rejects results after parent cancellation.
Sharing comes only from overlap. A
post's checks mostly overlap, since every interested stream wakes at once, but
staggered checks (lagging streams, render latency) each read: between one and
`N` reads per post, roughly the checks' arrival span over a read's duration.
- **Ordering:** a revocation committed before the check starts is in the
  read's snapshot; older memberships cannot allow, so a query denies. The
  installation-wide sequence preserves this across recreation. A commit
  after the read's snapshot can still let the event through: today's window,
  widened by the read's return.
- **Multi-process:** unchanged; every process reads the durable epoch, so
  there is no signal to lose.
- **Failure modes:** a read error stops the stream, as today. A new access
  table without a trigger leaves caches stale; a test listing the access
  tables can catch it. Staggered checks fall back towards `N` epoch reads
  per post: today's count, with a primary-key read instead of the join.

Not an option: taking the epoch from `EventsAfter`'s snapshot would cost
nothing, but that snapshot precedes the render, which is #262's bug.

### (b) One check per batch per wake

Check once after rendering a batch, before sending it. It breaks the
guarantee for every event after the first: event k waits behind k − 1
writes of up to `DefaultStreamWriteTimeout` (10 s) each, and a revocation
then goes unseen. It also saves nothing in the measured case: a caught-up
stream reads one new event per post, and only lagging streams read longer
batches ([stream cost](stream-cost.md)). Several processes change nothing.

### (c) One shared check per (member, organisation) per post

Streams of one account share the membership query under the same fresh-read
rule (joining a query already in flight would reopen #262's race). It keeps
the guarantee by today's argument and works per process, but costs a query
per account with an interested stream: at least `N / 16` (the per-account
cap), and close to `N` in real use, where a member has one or two tabs.

### Comparison

| | Keeps #262 | Several processes | Queries per post | Complexity | Main failure |
|---|---|---|---|---|---|
| (a1) | only for this process's own writes | NOTIFY or polling, which weaken it | 0 | invalidation in every writer | missed signal or direct SQL: stale |
| (a2) | yes, with the triggers | yes, unchanged | 1 to `N` epoch reads: few when checks overlap | column, triggers, fresh shared read | a new access table without a trigger; staggered checks |
| (b) | first event of a batch only | no change | about `N` | small | sends after revocation |
| (c) | yes | yes | at least `N / 16`, about `N` in use | small | still grows with streams |

## Decision

**(a2), with its triggers mandatory** (maintainer, 2026-10-08, on #622),
implemented by #669 and #670. It is the only option that keeps #262's
guarantee by the same ordering argument as today (the deciding read starts
after the render) and can stop the per-post cost following `N`. It also
covers direct SQL, today's only revocation path, and needs nothing new for
several processes. Its gain depends on checks overlapping: if measurements with
staggered checks show reads near `N`, (a2) only swaps the membership join
for a primary-key read, and the choice should be revisited.

- **Where it lives:** `org.Authorizer` keeps the cache and the shared read
  behind `MayReceive`, so `realtime`'s loop and its `Authorizer` interface do
  not change and `org` remains the only authorization logic.
- **Settled by the implementation issues:** joins do not bump (#669; only
  allows are cached), and a deny keeps today's skip-and-advance (#670; a
  removed member's open stream still queries per skipped event).
- Recorded here and in streaming.md; decision 23 covers the event log and
  hub. #669, #670 and #671 are high risk; #669 and #670 are tier A.

### Tests that prove (a2)

- **Unchanged:** the `realtime` tests above, #207's register-then-recheck
  and session cancellation.
- **`org`, no database:** an unchanged epoch runs no membership query
  (counted); a changed one queries and denies; a failed epoch read is an
  error, never a deny; organisation and audience checks still hold.
- **Shared read:** a check that arrives while a read is in flight gets the
  next read, forced with a gated store; two checks in sequence run two reads
  (nothing is retained).
- **PostgreSQL:** each trigger bumps the epoch in the writer's transaction,
  and a rolled-back write bumps nothing. A revocation committed while a
  membership load is in flight still denies the next event. Gated streams
  also cover warmed SQL removal, rename and same-ID/slug recreation.
  `TestEventStream` checks removal over SSE: the removed member misses the
  next event while the owner receives it.
- **Shutdown:** a gated successful epoch read with a waiting joiner fails
  closed after cancellation; cancelled membership results are never cached.

## Expected effect

About 15 shared queries per post end to end, plus the epoch reads: a few
per organisation **when the checks overlap**, up to `N` when they are
staggered. Misses reload independently; denies are never cached.

**Measured** (#671, [results](stream-authorization-results.md)):
- **Stream cost:** queries per post stay flat (16 to 46) instead of `N + 10`
  up to 10,000 streams; the highest passing step rose from 2,000 to 30,000
  (one member) and 11,000 (distinct members, past the cache's capacity).
- **Staggered checks** read 0.72 `N` per post at 1,000 streams with a 100 ms
  window and 0.056 `N` at 10,000; the revisit question is open on #671.
- **#216's active steps:** 10,000 passes with the default pool; at the next
  limit the pool was not the cause, and CPU was under pressure.
