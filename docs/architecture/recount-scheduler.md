# Bounded recount scheduler

Parts 1a–1b of #865 implement `realtime.RecountScheduler`, but neither
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
unchanged, without retry. A computation panic is recovered into the completion's
`Err`, including the panic value and stack trace, with empty output; the scheduler
does not log it. The worker stays alive and dispatches further eligible work,
including that registration's pending successor. This recovery covers the
computation on the worker, not goroutines started by it. The future writer owns error policy, generation capture,
version acceptance and authorization immediately before sending.

`Cancel` is idempotent: it removes pending work and queued eligibility, cancels
the computation context, drains and closes the mailbox, and suppresses late
results. After `Cancel` or scheduler `Close` drains and closes the mailbox,
receives return a zero `RecountCompletion` with `ok == false`, even if a
computation finishes later. A result already received belongs to the caller.
A running computation retains both worker and registration capacity until actual
return, even if it ignores cancellation. `Close` refuses new work, cancels every
registration and waits for actual returns; uncooperative computations can delay
it. These lifecycle operations support deterministic cleanup; application shutdown
is separate work.

Deterministic gates and `synctest` cover cancellation before dispatch, during
an uncooperative computation, after a buffered completion, before a late
completion and concurrently with return. They check immediate pending/queue
removal, context cancellation, closed empty mailboxes, admission retention and
release on actual return. With two occupied workers, cancelling one does not
start unrelated work; another worker's return does. The cancelled successor
never starts. Mutation commands and assertion evidence are in the part 1b PR
description; no production activation or shutdown behavior is claimed.

Scheduler instrumentation is deferred to a separate unit in original PR 2 of
#865, before production wiring or activation: queue length and oldest pending
age over time, running/waiting worker counts, processing time, and cancellation,
refusal and error counters. Including their contracts and deterministic tests
would exceed part 1b's review budget. Queue age must include a pending successor
behind a running computation, and cancellation must not report retained workers
as free. PostgreSQL connection and acquisition metrics belong to the wiring
unit; recount freshness and delivery latency belong to the writer measurements.
Posting, read and normal page latency belong to mixed-load stage 2 validation.
