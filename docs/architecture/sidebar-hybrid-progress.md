# Hybrid sidebar progress verification (#961, part 2c-b2)

The [delivery harness](sidebar-hybrid-delivery.md) exercises `Hub.Register`,
`Stream.Run`, `RecountScheduler`, the actual member watch and `Sender.Send`.
The external test adds gates and virtual time, with persisted posts every quarter
of `SidebarInterval`, including during snapshot computation and before delivery.
Each completion is checked before the next durable batch drains: releasing the
computation and one batch suffices, without advancing virtual time. A successor
recount also finishes while newer post/read generations remain dirty.

Both the candidate adapter and independent full-model oracle read the same
repeatable-read transaction pinned by the organisation sequence. Every sidebar
send, including connect and two subsequent cycles in each arm, matches that
boundary's scope, ordered lists, off-list selection, raw/capped counts and badges.
Separate assertions check pinned sequence, request-local watch coverage and the
live registration. Later persisted truth must differ; it is never the comparison
oracle for an earlier delivery. Fixtures include uncapped counts above 100,
50 listed topics, an unlisted selection and an empty channel.

The repair-only arm drops post triggers and persisted-read publications. An
unconditional test-only ticker wakes the real member watch at fixed 10/30/60s
sensitivity deadlines; it supplies invalidation, never count effects. Posting
cannot reset that clock. Two cycles verify no early request, capture exactly at
the fixed deadline and completion in the same virtual instant despite a durable
backlog. Lost reads exercise both a selected badge becoming empty and raw drift
hidden above the cap. This verifies test-only repair composition, not a production
periodic-repair feature or a performance/freshness measurement.

Together with 2a–2c-b1 this completes #961's verification. Mutation commands and
failures belong in the PR body. Production code, `SidebarInterval` and #865's
PR 3 plan are unchanged; measurement and adoption remain #952.
