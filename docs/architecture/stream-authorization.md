# Stream authorization cost

Keeping #262's guarantee without a membership query per connection/event.
`N` below is the number of interested streams; the earlier query costs and ceilings are in
[stream cost](stream-cost.md) and [load results](../measurements/load-results.md).

## #262's guarantee

For an event E and a connection of account A: if a transaction that removes
A's access to E commits **before the check for E starts** — before E was
read, while it waited in a batch, or while its render waited — E is not sent.
A removal committed **after the check has started** may let that one event
through (accepted on #262).

Denied-event behaviour is separate (#852): a deny of the stream's own member
ends the stream with `ErrMembershipRevoked`, without advancing the cursor;
another member's audience deny skips the event and advances. A failed check
ends the stream with the cursor before the event and is never a deny (see
`Run` in `internal/realtime/stream.go`).

Before #670, each interested stream ran a membership query per event with a
`READ COMMITTED` snapshot taken after rendering; the options compare against
that baseline, while the code now uses (a2).

## Options

### (a) Membership kept until the organisation's access changes

Each (account, organisation) membership is cached with an organisation-wide
**access epoch**; a check allows from memory while the epoch is unchanged and
runs the pre-#670 query when it changed. Whatever carries the signal, two rules
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
must be bumped by database triggers on every access-changing write, in the
same transaction as the change. Every new access-controlling table or write
path must get the triggers it needs in the migration that adds it.
Private channels and member removal remain [planned](../roadmap.md#after-the-mvp);
their access writers must obey these rules too. **The triggers are a required
part of (a2)** and cannot be replaced by epoch updates in application code:
direct SQL, another process or an older binary bypasses those updates.

A check takes the epoch from a read whose snapshot began after the check
started, and **an epoch value is never retained**: a stored or TTL-cached
epoch is stale. `realtime.Cache` gives this rule only with a `keep` that
rejects every value (`internal/realtime/cache.go`). Cancellation must fail
closed, and cancelled membership results must never be cached.
Sharing comes only from overlap. A post's checks mostly overlap, since every
interested stream wakes at once, but
staggered checks (lagging streams, render latency) each read: between one and
`N` reads per post, roughly the checks' arrival span over a read's duration.

- **Ordering:** a revocation committed before the check starts is in the
  read's snapshot; older memberships cannot allow, so a query denies. The
  installation-wide sequence preserves this across recreation. A commit
  after the read's snapshot can still let the event through: the pre-#670 window,
  widened by the read's return.
- **Multi-process:** unchanged; every process reads the durable epoch, so
  there is no signal to lose.
- **Failure modes:** a read error stops the stream and is never a deny. A new
  access table without a trigger leaves caches stale; a check enumerating access
  tables and their triggers is planned but unbuilt. Staggered checks fall
  back towards `N` epoch reads per post: the pre-#670 count, with a primary-key
  read instead of the join.

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
the guarantee by the pre-#670 argument and works per process, but costs a query
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

**(a2), with its triggers mandatory** (maintainer, 2026-10-08, on #622).
It is the only option that keeps #262's guarantee by the same ordering
argument as before #670 (the deciding read starts
after the render) and can stop the per-post cost following `N`. It also
covers direct SQL, today's only revocation path, and needs nothing new for
several processes. Its gain depends on checks overlapping: if measurements with
staggered checks show reads near `N`, (a2) only swaps the membership join
for a primary-key read, and the choice should be revisited.

Authorization decisions belong to `org`; `realtime` must not grow its own
authorization logic ([package boundary](packages.md)). Joins do not bump the
epoch because only allows are cached; denies are never cached. A rolled-back
access change must leave the committed epoch unchanged.

The revisit condition was exercised and (a2) kept on 2026-10-09; see the
[measurements and reasons](../measurements/stream-authorization-results.md).
