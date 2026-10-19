# Client unread count cost (#1047)

The default sidebar mode of [TestStreamCost](../architecture/stream-cost.md) now follows
[decision 34](../decisions/34-live-unread-counts.md). It uses author-only post
triggers, current-channel move triggers, the production `RecountLimit` and
`UnreadRenderer` selection in `Stream.Run`. Counts now use shared compose wiring; test adapters mirror only cmd/web
trigger and rendering behavior, with snapshot bytes replacing HTML.
`RIBBITTO_STREAM_COST_RECOUNTS` overrides the shared computation capacity;
unset, it is `max(1, pool size / 2)`, matching synchronous production.

`RIBBITTO_STREAM_COST_OTHER=0..100` places that percentage of streams on a
second channel in the same organisation (default 0; sidebar mode).
Each step rounds down: the last `floor(N * percent / 100)` streams move.
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
`Post` returning to send, the same origin as the no-unread baseline.
Final resync duration runs from publication to all streams becoming exact, including outstanding delivery. The delivery denominator includes both
frame kinds.

## Results (2026-10-15)

Measured by Claude on the baseline's machine: MacBook Pro Mac17,2, Apple M5
(10 cores), 32 GB, darwin/arm64, Darwin 27.0.0; Go 1.27.2; PostgreSQL 18.6
(Debian 18.6-1.pgdg13+2), local loopback port 55434. One run of the baseline's
ladder (1, 10, 100, 1,000, 5,000 streams), so 1,000 and 5,000 were each
measured once, with the baseline's warm-up: 20 posts/s for 4 s (80 posts),
pool 10, batch 100, distinct members, shared caches, no stagger, 50%
other-channel placement, recount limit 5 (half the pool). Commands as in the
[baseline](stream-sidebar-results.md), plus `RIBBITTO_STREAM_COST_SIDEBAR=1
RIBBITTO_STREAM_COST_OTHER=50`.

Every step completed all 80 posts, every expected `message` and `unread`
frame, and an exact final snapshot on every stream. Latency triplets are
p50/p95/p99 in milliseconds.

| Streams | Message (this run) | Message (no-unread baseline) | `unread` | Final resync | Recounts | SQL query/tx | Pool empty |
|---|---|---|---|---|---|---|---|
| 1,000 | 7.711/12.760/15.522 | 6.884/10.055/18.260 | 6.306/10.738/12.604 | 2.041 s | 1,004 | 13,140/4,150 | 0 |
| 5,000 | 28.373/37.857/42.475 | 22.031/49.370/86.006 | 22.328/32.243/38.645 | 5.035 s | 5,004 | 61,111/20,466 | 0 |

Initial snapshots are excluded. Recounts are each stream's final snapshot plus
four recounts of the posting member's own stream (author-only trigger, coalesced
once per second over 4 s), four count statements each; no other stream
recounted while posts flowed. SQL includes posting and the final resync.
For comparison, the server-recount approach measured 1.5 s median arrival at
5,000 streams and 20 posts/s ([results](stream-sidebar-results.md)).

At 5,000 streams the median arrival is 6 ms above the baseline, and p95/p99
are below it. Message arrival therefore stays close to the no-unread baseline,
the plan's target. The final resync wakes every member at once, so 5,000
snapshots queue behind the limit of 5 for about 5 s. Production spreads
resyncs with the jitter of #1046.

Limits: single short runs; the baseline was measured on 2026-10-11 with an
older build; here half the streams receive `unread` instead of `message`
frames, so the delivery work is not identical to the baseline's.
