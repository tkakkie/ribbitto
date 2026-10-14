# Bounded recount scheduler

Parts 1a–1d of #865 implement `realtime.RecountScheduler` and optional
`Stream.Recounts`; [command opt-in](../database.md#recount-capacity-configuration)
keeps synchronous sidebar delivery by default, with unchanged `SidebarInterval`.
This core adds no database access or deployment capacity.

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
computation on the worker, not goroutines started by it. The writer stops on
completion errors (including panics), wrapping them as synchronous recount errors.

`Stream.Run` registers once when configured, preserving its nil/synchronous path.
Part 2d adds `RunReserved`: the caller reserves with `Register` and transfers that
registration once, even across concurrent entries. It never registers again.
Nil, cancelled or consumed reservations return `ErrRecountRefused`; rejected reuse
does not cancel the first owner. A different non-nil `Stream.Recounts` scheduler
also returns that error and cancels the transferred reservation. With `Recounts`
nil, the reservation supplies scheduling. The caller must cancel if it never hands
off. Without sidebar interest or a source, entry cancels immediately before reading.
Every transferred reservation is cancelled on exit, including negative cursors,
watch/reader panics, resets, errors and cancellation. Watch has no error return.
The shared loop seals computation before watch release; capacity still waits for
actual computation return. Reserved compatibility tests retain part 1e/1f's
cursor, watch-publication and fresh-authorization assertions. The web stream
handler pre-admits (part 2e, [streaming](streaming.md#authorization-and-revocation));
command composition requires explicit opt-in.

The writer cancels on every exit and releases its watch on refusal with
`ErrRecountRefused`. It consumes completions while idle and between batches,
accepting only versions newer than the last sent; later invalidations stay dirty.
Each completed attempt starts the unchanged `SidebarInterval`. Computations capture
the watch generation at their boundary, guarded against access after release;
each request owns its capture, indexed by version until its completion is received.
The mailbox receive publishes that request-local value to the writer; another
computation cannot overwrite it. Production submits only while inactive, bounding
retained captures to one. Any received completion clears active and sets the next
deadline, including duplicate/older versions that send nothing. Only the sent
request's generation is covered by delivery. The writer checks parent cancellation,
then bound membership immediately before each id-free sidebar frame. Durable
delivery and heartbeats continue during queued/running work; late results cannot send.

`Cancel` is idempotent: it removes pending work and queued eligibility, cancels
the computation context, drains and closes the mailbox, and suppresses late
results. After `Cancel` or scheduler `Close` drains and closes the mailbox,
receives return a zero `RecountCompletion` with `ok == false`, even if a
computation finishes later. A result already received belongs to the caller.
A running computation retains both worker and registration capacity until actual
return, even if it ignores cancellation. `Close` refuses new work, cancels every
registration and waits for actual returns; uncooperative computations can delay
it. Application shutdown is below.

Deterministic gates and `synctest` cover cancellation before dispatch, during
an uncooperative computation, after a buffered completion, before a late
completion and concurrently with return. They check immediate pending/queue
removal, context cancellation, closed empty mailboxes, admission retention and
release on actual return. With two occupied workers, cancelling one does not
start unrelated work; another worker's return does. The cancelled successor
never starts. Mutation commands and assertion evidence are in the part 1b PR
description; no production activation or shutdown behavior is claimed.

Part 1e reruns delivery cases with a non-nil scheduler: message and owner
scope filters, organisation-wide sidebar triggers, cold-hub batches, replay
resets and error/cancellation cursor rules retain their synchronous assertions.
Gates prove presence and typing continue while a recount is queued and running;
owner connect tokens/generations and later owner resets preserve the cursor.
Sidebar, presence and typing frames omit IDs. Future-cursor resets also check
configured read limits. Part 1f-a gates subscription before the initial recount,
publications while queued, computing, completing, sending and capturing a wait,
and release/late-worker safety. It also delivers a held completion after a successor
has captured, proving request-local coverage independently of one-active submission.
Mutation commands and failing assertions are in its PR description. Expanded
authorization coverage in part 1f-b reuses synchronous safety cases and gates
revocation during recounts and durable renders, with cancellation/error races.
Fresh bound-member checks precede each frame, reset and heartbeat; another
audience's deny advances, while bound revocation and check errors preserve the
affected durable cursor. Pending and late output cannot survive exit.
Part 2h composes opt-in; PR 3 decides production activation.

Part 2a adds `Stats() RecountStats`, a read-only, allocation-free snapshot
sampled under the scheduler mutex. It copies lifetime counters and scans the
bounded registration set; no request histories or observations are retained.
`SnapshotAt` timestamps its gauges; ages are zero when no matching work exists.
Pending includes a successor behind running work. Replacement preserves the
start of continuous pending time, even before eligibility. Eligible counts only
registrations in the worker queue. Running workers remain occupied after
cancellation until actual return; waiting workers are idle in the scheduler's
condition wait (exited workers are neither). Registrations includes capacity
retained by cancelled running work. Both configured limits and closure are visible.

Completed counts every actual computation return, including cancelled work,
errors and recovered panics, regardless of mailbox replacement or delivery.
ProcessingDuration sums dispatch-to-return time, sampled before reacquiring the
mutex. DurationBuckets holds six disjoint counts with inclusive upper bounds
of 1ms, 10ms, 100ms, 1s and 10s, then an overflow bucket; their sum is Completed.
OldestRunningAge includes unfinished and cancelled work, preventing stalled work
from disappearing from processing summaries.

Cancelled increments once per pending computation removed and once per running
computation first cancelled. Replacement, buffered-result disposal and repeated
Cancel/Close add nothing. RefusedCapacity counts full Register calls;
RefusedClosed counts closed Register calls, taking precedence over capacity.
RejectedSubmissions counts each Submit refused for a nil computation, cancelled
registration or closed scheduler. Errors counts returned errors and recovered
panics from live registrations. ErrorsAfterCancellation counts those whose
registration is already cancelled (`r.closed`) when the worker processes the
return under the scheduler mutex, regardless of the error value. The counters
are disjoint: even `context.Canceled` counts in Errors if the registration is
still live. Cancellation racing with return is classified by that mutex-protected
check; cancellation that lands after return processing counts as a live error.
All counters persist after Close.

Gated synctest tests cover these rules and duration boundaries without sleeps;
mutation commands and failing assertions are recorded in the part 2a PR body.
Part 2b exposes these fields through the optional scheduler dependency in
[development metrics](dev-metrics.md#scheduler-snapshot), splitting live errors
(`errors`) from errors after cancellation (`errors_after_cancellation`). Its gated
error/panic tests and mutations cover both classifications, including cancellation
after processing; commands and failing assertions are in the part 2b PR body.
Default delivery remains synchronous. Processing time is
not recount freshness; mixed-load validation remains later work.

Command capacity, observation and shutdown composition are in
[recount operations](recount-operations.md).

## Current shared computation limit (#1046)

Production shares one `RecountLimit` across connect, read, resync and move
recounts in both modes, separately from scheduler registrations. Slot acquisition
waits at most 25 ms. Expiry is a deferral, not a stream error: dirty state and
watch coverage remain pending, and a new attempt follows after 250–1,000 ms
of jitter. Slots are released only when the actual computation returns.
The asynchronous worker returns a deferred completion; only the stream writer
schedules its retry. Neither mode sends 503 on a deferred established stream.

Connect sets a recount deadline randomly within 0–3 s rather than blocking
the writer. Durable frames, owner frames and authorization continue during
that delay. A connect occupies no computation slot until its deadline.
Pool sizing is in [recount capacity](../recount-capacity.md#current-computation-sizing-1046).
