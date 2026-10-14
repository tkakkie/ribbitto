# Client unread count cost (#1047)

The default sidebar mode of [TestStreamCost](stream-cost.md) now follows
[decision 34](../decisions/34-live-unread-counts.md). It uses author-only post
triggers, current-channel move triggers, the production `RecountLimit` and
`UnreadRenderer` selection in `Stream.Run`. Test adapters mirror only wiring
that cannot be imported from cmd/web; snapshot bytes replace HTML.
`RIBBITTO_STREAM_COST_RECOUNTS` overrides the shared computation capacity;
unset, it is `max(1, pool size / 2)`, matching synchronous production.
The stopped comparison arms retain their historical wiring and knobs.

`RIBBITTO_STREAM_COST_OTHER=0..100` places that percentage of streams on a
second channel in the same organisation (default 0; sidebar mode, no comparison
arm). Each step rounds down: the last `floor(N * percent / 100)` streams move.
Each posting-channel stream must receive exactly one `message` per post;
each other-channel stream exactly one shared `unread`, with no `message`.
The initial snapshot still checks alternating read-state seeds (0/1);
other-channel topics and that empty channel must have zero unread messages.

After posts finish, one `unread.Changes.RequestResync` per member wakes the
same member watch as the resync POST. Completion requires every posted sequence
on every stream and a snapshot computed after that publication, with exact
capped count totals and the final committed sequence. Missing, duplicate or
wrong-placement frames and inexact snapshots fail, including in smoke mode.
SQL totals include posting and final resync; initial recounts remain excluded.
Message arrival and unread delivery have separate p50/p95/p99 samples from
`Post` returning to send, the same origin as the no-unread baseline. Final resync duration runs from publication to all streams becoming
exact, including outstanding delivery. The delivery denominator includes both
frame kinds. Historical recount-delay samples apply only to comparison arms.

## Results (Claude fills after review)

Pending: one run each at 1,000 and 5,000 streams, 20 posts/s for 4s, pool 10,
distinct members, shared caches, no stagger, 50% other-channel placement.
Record host/software, message and unread latency, final resync duration, frame
counts, SQL/pool totals and completion. Compare message arrival with the
[no-unread baseline](stream-sidebar-results.md): p50/p95/p99 at 1,000 streams
6.884/10.055/18.260 ms, at 5,000 streams 22.031/49.370/86.006 ms.
