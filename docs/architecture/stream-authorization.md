# Stream authorization cost

How a stream could keep #262's guarantee without one database query per
connection per event (#622). **This is a proposal, not a decision:** the
maintainer picks an option. Until then the rule in
[streaming](streaming.md#authorization-and-revocation) stands (one
membership query per event), and nothing here is implemented.

## Today

- **The check:** `Stream.deliver` (`internal/realtime/stream.go`) filters,
  renders, calls `Authorizer.MayReceive`, then sends.
  `org.Authorizer.MayReceive` (`internal/org/authorizer.go`) runs
  `GetMembershipBySlug` (`db/queries/org/member.sql`) every time, then checks
  the organisation and the audience in memory. A failed query stops the
  stream; it is never a deny.
- **The cost:** a post wakes every stream of its organisation, and each
  caught-up stream checks it once, so a post costs one query per open stream
  plus shared work. With #227's caches the benchmark measured `N + 10`
  queries per post and passed 2,000 streams ([stream cost](stream-cost.md)).
  End to end at 10 posts/s with pgx's default pool of 10, 7,000 streams pass
  (7,015 queries per post) and 10,000 fail, with p95 at 2.8 s over HTTP/1.1
  and 4.3 s through Caddy over HTTP/2. A 40-connection pool passes 10,000 and
  fails 15,000 ([load results](load-results.md)): it moves the ceiling, not
  the slope.
- **What revokes access:** nothing in the application yet. No code removes a
  member, changes a role, or renames or deletes an organisation, and every
  channel is public; foreign keys stop deleting an organisation, or a member
  who has posted. Only direct SQL removes a
  membership, as #262's reproduction did. Private channels and member
  removal ([roadmap](../roadmap.md#after-the-mvp)) will add the real writers.
  Sign-out is separate: `Hub.CancelSession` ends the session's streams, with
  no query per event.

### #262's guarantee, testable

For an event E and a connection of account A: if a transaction that removes
A's access to E commits **before the check for E starts** — before E was
read, while it waited in a batch, or while its render waited on the
database — E is not sent and the cursor moves past it. A commit after the
check can still let that one event through: only the write remains between
check and connection (accepted on #262). Today this holds because the check
is a `READ COMMITTED` query whose snapshot is taken after the render
returns. `TestStreamRechecksAccessBeforeEachSend` and
`TestStreamDeniesAccessLostWhileRendering`
(`internal/realtime/stream_test.go`) pin it. Every option must keep them
unchanged and add a test for each ordering it introduces.

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
path), another process, or an older binary during a deploy is never seen,
and the cache stays stale until a restart. Across processes, LISTEN/NOTIFY
arrives after the commit, so a check between commit and arrival allows, and a
dropped listener connection loses signals; polling bounds staleness to its
period. Both weaken "committed before the check" to "committed a while before
the check".

**(a2) Durable epoch, read fresh and shared.** `organization.access_epoch`
is bumped by triggers on the tables that decide access (`member` deletes and
updates of role, organisation or account; `organization` slug updates and
deletes; later the private-channel tables), so direct SQL and other binaries
bump it too. A check takes the epoch from a read whose snapshot began after
the check started. `realtime.Cache` already has that rule
(`internal/realtime/cache.go`: callers that join a load in flight share a
second load started after it finishes), so all streams of the organisation
share each read.
- **Ordering:** a revocation committed before the check starts is in the
  read's snapshot; the epoch differs, so the query runs and denies. A commit
  after the read's snapshot can still let the event through: today's window,
  widened by the read's return.
- **Multi-process:** unchanged; every process reads the durable epoch, so
  there is no signal to lose.
- **Failure modes:** a read error stops the stream, as today. A new access
  table without a trigger leaves caches stale; a test listing the access
  tables can catch it. The cheap read can still queue behind a busy pool.

Not an option: taking the epoch from `EventsAfter`'s snapshot would cost
nothing, but that snapshot precedes the render, which is #262's bug.

### (b) One check per batch per wake

Check once after rendering a batch, before sending it. It breaks the
guarantee for every event after the first: event k waits behind k − 1
writes of up to `DefaultStreamWriteTimeout` (10 s) each, and a revocation
then goes unseen; keeping the guarantee means a check before each send,
which is today's design. It also saves nothing in the measured case: a
caught-up stream reads the one new event per post, and only lagging streams
read longer batches ([stream cost](stream-cost.md)), so it helps only once
overloaded. Several processes change nothing.

### (c) One shared check per (member, organisation) per post

Streams of one account share the membership query under the same fresh-read
rule (joining a query already in flight would reopen #262's race). It keeps
the guarantee by today's argument and works per process. But it still
costs a query per account with an open stream: at least `N / 16` (the
per-account cap), and close to `N` in real use, where a member has one or two
tabs. One query for all connected members of an organisation would be O(1)
queries, but its rows still grow with `N`.

### Comparison

| | Keeps #262 | Several processes | Queries per post | Complexity | Main failure |
|---|---|---|---|---|---|
| (a1) | only for this process's own writes | NOTIFY or polling, which weaken it | 0 | invalidation in every writer | missed signal or direct SQL: stale |
| (a2) | yes | yes, unchanged | a few per organisation | column, triggers, shared read | a new access table without a trigger |
| (b) | first event of a batch only | no change | about `N` | small | sends after revocation |
| (c) | yes | yes | at least `N / 16`, about `N` in use | small | still grows with streams |

## Recommendation (for the maintainer to decide)

**(a2).** It is the only option that both stops growing with the number of
streams and keeps #262's guarantee by the same ordering argument as today:
the deciding read starts after the render. It also covers direct SQL, today's
only revocation path, and needs nothing new for several processes. (a1)
saves a few reads per post but trades away exactly those cases.

- **Where it lives:** `org.Authorizer` keeps the cache and the shared read
  behind `MayReceive`, so `realtime`'s loop and its `Authorizer` interface do
  not change and `org` remains the only authorization logic.
- **Left to the implementation issue:** whether a removed member's open
  stream ends rather than querying for every event it skips; whether joins
  bump the epoch (only allows are cached, so granting access needs no bump);
  triggers versus explicit bumps in each writer.
- **Recording it:** the pick replaces streaming.md's rule. No settled
  decision covers the per-event check (decision 23 covers the event log and
  the hub, which (a2) leaves alone), so a decision record is needed only if
  the pick changes one. The implementation becomes its own issue or issues,
  high risk and adversarial tier A; #231 already lists most of the required
  checks and can be rewritten to the pick.

### Tests that would prove (a2)

- **Unchanged:** the `realtime` tests above, #207's register-then-recheck
  and session cancellation.
- **`org`, no database:** an unchanged epoch runs no membership query
  (counted); a changed one queries and denies; a failed epoch read is an
  error, never a deny; organisation and audience checks still hold.
- **Shared read:** a check that arrives while a read is in flight gets the
  next read, forced with a gated store.
- **PostgreSQL:** each trigger bumps the epoch in the writer's transaction,
  and a rolled-back write bumps nothing; the membership and the epoch come
  from one statement; a membership deleted by plain SQL while a render is
  blocked stops that event.

## Expected effect

About 15 shared queries per post end to end, plus a few epoch reads per
organisation, **whatever the number of streams**; an access change costs
one membership query per cached (account, organisation), once.

- **Stream cost:** rerun `TestStreamCost` with `_CACHE=1` at 1,000 to 5,000
  streams and beyond. Queries per post should stay roughly flat instead of
  `N + 10`, queries per delivery fall toward zero, and the highest passing
  step rise above 2,000 until something other than the pool limits it.
- **#216's active steps:** queries per post near constant at 7,000 and
  10,000 (today 7,015 and 10,012). 10,000 should pass with the default pool,
  and the series continues (15,000, 20,000…) to find the next limit, likely
  CPU (the server used about 3.5 CPUs at 10,000) or Caddy.
