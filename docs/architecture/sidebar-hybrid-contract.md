# Hybrid sidebar investigation contract (#950)

Test-only contract for #950 → #951 → #952, fixed before code or measurement.
Production async remains off; this does not change #865's
[stage-2 protocol](stream-sidebar-stage2.md), production events or intervals.
Counts follow [unread rules](../domain/unread.md) and
[persisted writes](unread-writes.md).

## Derivability

The production-only condition is mandatory and reported separately from every
hypothetical condition. The durable envelope supplies organisation and sequence;
`conversation.Posted` supplies only channel, message and posting-time topic
(absent in legacy events). Read publications supply only organisation and member,
possibly after an unknown commit outcome. No fixture knowledge may fill these
gaps silently. Knowing IDs or an unchanged channel total is not a full delta.

| Operation | Current production events only | Hypothetical information and required production change |
|---|---|---|
| Ordinary post, including another channel | Reconcile: author unknown, own post is read; the author's page-scope reads can also reduce existing counts. | Add author to committed post payload; non-author +1 needs exact prior state and complete event coverage. Author still needs committed read effects. |
| Post with feed/topic cursor; older-page post without cursor | Reconcile: no cursor, scope or read effects; absence cannot be inferred. | Extend the posting transaction's event with author and committed newly read message identities/ranges plus scope; maintain message/read identity state to subtract only previously unread messages. Author alone is insufficient. |
| Feed or topic read, fragmented ranges, join prefix, overlapping/repeated multi-tab reads | Reconcile: publication is invalidation, not a decrement or successful-read receipt. Topic reads can leave other topics unread even at the latest cursor. | Add commit-confirmed, ordered member read revision and exact read-set union/message coverage to Reading's publication; persist/replay revisions and snapshot them. Unknown-outcome publications still reconcile. Scope/cursor alone cannot resolve moves or overlaps. |
| Move/branch of read or unread messages | Reconcile: `conversation.Moved` gives IDs and source/destination but no per-member read status, original sequences or new topic metadata. Channel total of moved messages is unchanged; the branch notice is a separate post with author read effects. | Committed move payload needs original sequences, topic metadata and a complete cached message/read set; apply per-member unread transfers and separately cover the notice. Otherwise supply exact committed per-member effects. |
| Topic-list displacement, new/renamed channel/topic, unlisted selection | Reconcile: post/move IDs do not describe fresh names/order or the full list. | Publish committed list metadata/revisions and maintain the complete ordered list, including off-list topics; snapshot the same revision. |
| Cap crossing and clearing zero badges | Reconcile for the operations above; a capped count cannot reveal the uncapped value. | No extra cap event: exact uncapped state and the operation metadata above allow recomputing display as min(count,100), zero empty, 100 = 99+. |
| Another member/organisation's read or unrelated durable event | Exact no-op only when the operation provably cannot affect this sidebar; still advance durable coverage. | None; membership/revocation events require authorization, never an inferred allow. |
| Connect/reconnect, periodic repair, lost/reordered input, unknown payload | Reconcile regardless of apparent counts. | No delta-only substitute; metadata cannot repair already lost state. |
| Muting/private membership drift | Hypothetical only: neither feature exists; reconcile injected drift. | New visibility/muting model, scoped notifications and snapshot authorization; never mix this arm with production-only results. |

Thus today's ordinary posts and reads are **not delta-only**. Savings may be
small; #952 must report that, even if richer synthetic metadata performs well.

## Coverage boundary

A snapshot carries stream incarnation, request generation, organisation sequence
S from its repeatable-read database snapshot, and member watch generation G
captured before pinning that snapshot. Subscribe before initial capture. G covers
invalidations observed before capture, not proof of committed read coverage:
an unknown-outcome publication can precede its commit. Publications after capture
remain dirty, even when their writes happen to appear in the snapshot.

Keep the durable replay cursor separate from applied sidebar coverage. Consume
the complete organisation sequence, including no-op events: that sequence detects
a missing durable delta; a gap/reset reconciles. A filtered post-only sequence
cannot distinguish unrelated events from loss. Duplicate sequences already
covered by S or applied once are ignored; reorder buffers must be bounded and
gaps trigger repair. Read-only changes have no durable event sequence; local
watch generations detect observed invalidation, not lost publication. Any
hypothetical read delta therefore needs its own persisted/replayable revision.

Only the writer applies/sends. Accept a progressing snapshot even while newer
triggers arrive; keep later dirt pending. Never require desired-generation
equality, which can starve repair. Before replacing state, replay retained exact
deltas after S; if unavailable, reconcile and do not roll back already applied
state. Older/duplicate request generations cannot replace newer state. Cancel
work on exit, authorize the bound member immediately before every send (even
after audience deny), and preserve durable send order. G and request generations
live only within one subscription/incarnation; reconnect resets them, rejects
late old completions, and takes a fresh snapshot. S remains organisation-scoped.

Periodic repair runs even without events: base 30 s, separate 10 s/60 s
sensitivity, fixed deadlines unaffected by new posts. Deterministic tests use
synctest or gates. Exact derivable updates must match immediately after applying;
non-derivable updates must match when reconciliation completes. Test continuous
posting progress and the before-capture/during-compute/before-send boundaries.

## Exact model and comparison

The independent persisted-state oracle reads messages and read ranges in one
repeatable-read snapshot, using the persisted join prefix, never count adapters,
topic-floor optimizations, event payloads or candidate state. It compares ordered
channel entries, 50 ordered current-channel topic entries, selected topic even
when unlisted, scope, every uncapped/capped count and empty zero badge. Channel
totals cover all topics. Floored scans are an optimization, not read truth.
Missing, extra, wrong-other-channel and stale read-only entries fail equality.
At concurrent checkpoints compare against the same pinned persisted boundary;
never compare a delayed snapshot to an arbitrary later database state.

(a) is #832 stage 1's async shared `costWriter`/`costQueue`, pool 36, 26 workers;
(b) uses the same harness with `RecountScheduler` reconciliation. Match 36/26,
registration bounds, caches, batch, authorization, fixtures, seeds and telemetry.
Extend both arms identically for member watches/full exact models; do not replace
(a) with production async or silently replace its scheduler. Extra test-only
uncapped/witness SQL must be identical, counted and distinguished from oracle
verification work outside application timing/totals. Both need exact state proof.

#952 compares 5,000 streams at 5/20 posts/s and 100 **persisted changed** reads/s;
successful no-ops do not satisfy the rate. Windows include posts, reads and drain;
initial recounts/fixtures are excluded. Report posting latency, delivery p95,
SQL, computations, snapshot/delta sends, leases, queue length/oldest age, drift
count/max and persist-to-exact freshness (p95 ≤2 s, p99 ≤5 s). Separate delta
freshness from periodic repair, and normal operation from lost-input recovery;
expose 30 s repair target misses. Retain delivery p95 ≤250 ms, complete scheduled
operations, zero missing deliveries/final snapshots/timeouts/loop errors and the
[unchanged stability rule](stream-sidebar-stability.md#new-window-rule-and-telemetry).
Judge (b) on every gate independently even if (a) fails; weigh latency, freshness,
correctness, stability, connections and SQL together. First one 60 s smoke is
inconclusive for stability. Adoption at 30 s needs three independent passing
600 s windows at both rates; all long-run/sensitivity scope requires explicit
maintainer authorization. #950 performs no prototype or measurement.

## Baseline prototype (#951, part 2a)

The external realtime tests implement an independent repeatable-read full-state
adapter and a model writer using `RecountScheduler` (26 workers). A controllable
virtual clock drives unconditional fixed repair deadlines at 10/30/60 seconds;
completion mailboxes and a computation gate provide deterministic checkpoints.
Records label each operation `production-only`, with a reconciliation request or
an exact no-op delta. No author/cursor/read-effect metadata is inferred. The
baseline covers connect/reconnect, uncapped and displayed state, quiet repair and
non-derivable read completion. Cost-arm integration and measurements remain #952.

## Production domain checks (#960, part 960a)

The same prototype checks persisted post/author/page-read effects, fragmented and
overlapping multi-tab reads, join prefixes, feed/topic differences, both cap
crossings, zero badges, all-topic totals, 50 listed topics and unlisted selections,
list displacement, read/unread moves with a separate notice, other channels and
member/organisation isolation. Synthetic persisted fixtures express these domain
effects; they do not test the production publishing transactions or add metadata
to production events. Every operation records its routing reason. Isolated input
is an exact no-op immediately; relevant input reconciles.

Writes finish before each oracle boundary and remain stopped until all tab
computations complete; gates check unchanged state before completion. Both raw
and capped full models must match. Lost delivery and injected duplicated count
and reordered topic effects must exhibit drift, then match after a virtual-clock
periodic repair. These injections do not claim production delta derivability or
prove delivery order. Labelled hypothetical metadata is covered below; ordering,
generations, no rollback and continuous-posting progress remain #961.

## Hypothetical domain checks (#960, part 960b)

The same scenarios run separately with author, commit-confirmed read coverage,
or a read-transaction channel total. Derivable deltas match immediately; other
changes reconcile. Metadata, fixture revisions and remaining limits are in
[the focused verification note](sidebar-hybrid-hypothetical.md).
