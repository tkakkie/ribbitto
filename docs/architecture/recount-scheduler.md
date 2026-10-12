# Bounded recount scheduler

Part 1a of #865 implements `realtime.RecountScheduler`, but neither
`Stream.Run` nor `cmd/ribbitto` uses it. Production keeps synchronous
sidebar delivery and the unchanged `SidebarInterval` ([streaming](streaming.md)).
This core adds no database access, deployment capacity or shutdown wiring.

One shared instance has positive worker and registration limits. `Register`
returns nil at capacity or after closure. A registration represents one stream;
its owner must call `Cancel` on every exit. There is no request-context observer
or bridge goroutine per registration.

`Submit` accepts a caller-owned version and a computation receiving the
registration's cancellation context. Each registration has at most one running
and one pending request. New submissions replace pending work. Idle registrations
enter a FIFO eligibility queue; replacement keeps that position. A running
registration becomes eligible at the tail only after its computation returns.
A fixed worker set runs at most the configured number of computations concurrently;
multiple workers may begin execution in a different order from dispatch.

Workers compute only: they receive no sender and do not authorize or deliver.
Output, version and error go into a one-slot completion mailbox, replacing any
unread completion. A stalled consumer cannot retain a worker. Errors are returned
unchanged, without retry; the future writer owns error policy, generation capture,
version acceptance and authorization immediately before sending.

`Cancel` is idempotent: it removes pending work and queued eligibility, cancels
the computation context, drains and closes the mailbox, and suppresses late
results. A result already received belongs to the caller. A running computation
retains both worker and registration capacity until actual return, even if it
ignores cancellation. `Close` refuses new work, cancels every registration and
waits for actual returns; uncooperative computations can delay it. These lifecycle
operations support deterministic cleanup; application shutdown is separate work.
