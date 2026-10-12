# Sidebar recount scheduler (#832, PR 2)

Part of #832, stacked on PR #836 (`codex/recount-independence`). This adds only
the test-local scheduler and its safety evidence. It establishes no writer
independence, performance result, production design or pool configuration.
The [pool protocol and earlier evidence](stream-sidebar-pool-results.md) remain
the baseline. The maintainer approved PRs 2–4 as separate units under #832.

PR 3 now records the [single-writer contract and delivery evidence](stream-sidebar-writer-results.md),
including #859 membership revocation and the remaining production gaps.
The remaining-integration section below records PR 2's original handoff.

## Contract

`internal/realtime/cost_queue_test.go` is in realtime's external test package.
Workers accept closures returning `realtime.Outgoing` and an error; they never
authorize or send frames. Construction requires positive worker and registration
bounds. Registration returns nil at capacity or after shutdown. Each successful
registration denotes one stream, irrespective of member identity; duplicate
registration of a connection must be prevented by the future writer.

One mutex orders admission, replacement, cancellation and completion publication.
Each stream has at most one running request and one pending request. A later
accepted submit replaces the pending closure/version without changing its first
waiting time or FIFO position. Submit returns false for a foreign, cancelled or
shutdown registration. "Newer" means admission order; the caller supplies
monotonically increasing versions for later delivery guards.

Idle streams enter the ready FIFO on their first request. While a stream runs,
its pending request is ineligible. When that run returns, the stream joins the
FIFO tail behind all already eligible streams, even if its pending request was
submitted earlier. Replacement cannot let a busy stream displace a quiet waiter.
With multiple workers, FIFO governs dequeue/admission, not wall-clock compute
start or finish order across workers. The scheduler has no timers or interval
policy; PR 3 must enforce the unchanged `realtime.SidebarInterval`.

For W workers and N registrations, running requests are at most min(W,N), pending
requests at most N, ready entries at most N, and retained completions at most N.
There are exactly W worker goroutines and no per-submit goroutines. Each stream's
completion channel has capacity one; an unread completion is replaced by the
next completion, including its version, payload and error. A stalled consumer
cannot pin a worker. Channels remain open; the writer will select its own
disconnect context as well as the completion channel. These are item bounds,
not byte limits on closures or rendered payloads. Registry and ready slices
release cancelled entries rather than retaining every historical stream.

Cancellation is explicit and idempotent: the future writer must call
`cancel` on disconnect (including parent cancellation). It cancels the
registration's computation context, physically deletes any ready entry and
pending closure, drains an existing completion and suppresses later publication.
Idle registrations free capacity immediately. A running registration retains its
slot until the closure returns, so cancellation cannot admit unbounded still
returning jobs. Computations must cooperate with their context; shutdown cannot
force a closure to return. Shutdown refuses admission, cancels every registration,
wakes idle workers and joins all workers, including a closure returning after
its cancellation. Repeated cancellation/shutdown is safe.

## Counters for PR 4

`stats` takes a consistent mutex-protected snapshot. `queued` counts pending
requests, including the pending successor of a running stream; `registered`
includes cancelled runs still returning. `oldest` is the maximum elapsed time
since those requests first became pending, zero with no pending work. Thus it
includes wait for same-stream serialization as well as worker capacity and is
not solely the age of the ready FIFO's head. Replacement preserves that age.

`coalesced` counts discarded pending requests, excluding replacement of unread
completions. `cancelled` counts discarded pending and cancelled running requests
once each; a stream with both contributes two. Cancelling an idle registration
or repeating cancellation adds zero. These totals last for the queue's lifetime.
PR 4 must sample both pools when partitioned, across posting and drain, and
disqualify sustained growth; there is no ladder integration or growth verdict yet.

## Deterministic evidence and mutations

All five `TestCostQueue*` tests use channel gates and `testing/synctest`.
The only sleep advances fake time by two seconds to test oldest waiting age;
it does not synchronize requests. The burst races 32 registrations at a cap of
four and 400 submissions across those four, with two gated workers. It proves
same-stream exclusion even with a spare worker, and exact replacement and
cancellation totals. The shutdown test holds a cancelled computation before
returning, proving that shutdown waits and registration capacity stays occupied.
The mailbox test leaves 20 sequential completions unread and checks the newest
payload/version/error without blocking the worker, then checks cancellation
drains an already-computed completion.

Export the worktree-local Go caches as in the
[pool protocol](stream-sidebar-pool-results.md#reproduce),
`RIBBITTO_TEST_DATABASE_URL=<local test database>` and `RIBBITTO_REQUIRE_DB=1`.
Normal verification:

```sh
go test -race -count=1 -run '^TestCostQueue' -timeout 30s ./internal/realtime
```

Each mutation below was applied alone to the scheduler, run with the exact
matching command below, then restored. Each exited 1 at a test assertion,
without a compile failure, panic or timeout. The final normal run passed.

```sh
go test -race -count=1 -run '^TestCostQueueFairReplacement$' -timeout 10s ./internal/realtime
go test -race -count=1 -run '^TestCostQueueBoundedBurst$' -timeout 10s ./internal/realtime
go test -race -count=1 -run '^TestCostQueueCancellation$' -timeout 10s ./internal/realtime
go test -race -count=1 -run '^TestCostQueueMailbox$' -timeout 10s ./internal/realtime
go test -race -count=1 -run '^TestCostQueueShutdownJoins$' -timeout 10s ./internal/realtime
```

| Test suffix | Deliberate defect | Actual failing assertion |
|---|---|---|
| FairReplacement | Prepend returning busy stream instead of appending | `FIFO/latest admission = 3, want 10` |
| FairReplacement | Assign pending request only if nil | `FIFO/latest admission = 2, want 3` |
| FairReplacement | Reset waiting timestamp on replacement | `replacement reset wait age or grew queue`, oldest=0 |
| FairReplacement | Increment coalesced by zero | Same assertion, coalesced=0 |
| FairReplacement | Increment queued snapshot by zero | Same assertion, queued=0 |
| BoundedBurst | Refuse only when registrations > limit | `registered burst = 5, want cap 4` |
| BoundedBurst | Start W+1 workers | `burst exceeded worker/per-stream bounds`, starts=1 |
| BoundedBurst | Queue a running stream immediately | `same stream ran concurrently` |
| BoundedBurst | Append another ready entry on replacement of an idle stream | `burst exceeded worker/per-stream bounds`, starts=0 (ready length exceeded two) |
| Cancellation | Leave cancelled entry in ready slice | `queued cancellation retained request` (ready entry still present) |
| Cancellation | Omit context cancel | `in-flight cancellation failed to stop work or published completion` |
| Cancellation | Increment cancelled by zero | `queued cancellation retained request`, cancelled=0 |
| Mailbox | Increase channel capacity to two | `completion mailbox grew or pinned worker: 1` (capacity check) |
| Mailbox | Publish only when mailbox is empty | `latest completion/error lost`, version=1 |
| Mailbox | Publish nil error (retain err with `_ = err`) | `latest completion/error lost`, err=nil |
| Mailbox | Omit cancellation's mailbox drain | `cancelled registration retained completed mailbox` |
| ShutdownJoins | Omit worker Wait | `shutdown returned before worker exit` |
| ShutdownJoins | Omit context cancel | `shutdown did not cancel active context` |
| ShutdownJoins | Delete registration before running closure returns | `shutdown admitted work or freed a still-running registration` |
| ShutdownJoins | Remove closed check from register | `shutdown leaked work/completion` (later registration accepted) |
| ShutdownJoins | Publish completion in closed-stream branch | `shutdown leaked work/completion` (mailbox nonempty) |

An initial capacity-two mutation survived because the implementation still
retained only one item; adding the explicit capacity assertion exposed the
allocation contract. No existing test changed. Claude review and tier A / Grok
remain for the external orchestrator; no other AI CLI was started here.

## Remaining integration

PR 3 must return completions to the sole writer, wake a quiet writer without a
durable event, and keep the one-second minimum after each completed attempt.
Its gated tests must prove same-stream message progress while queued/running,
reject completions older than sent and pending snapshots, cancel on disconnect
with no later send, authorize after computation and immediately before every
send, preserve durable message IDs/order, and keep sidebar frames id-free.
Mailbox replacement alone proves none of those delivery properties.

Document the test-local loop's production gaps explicitly: combined completion
wakes, recount deadlines, heartbeat timing, error handling, ephemeral owners,
replay-contiguity and delivery filtering, and process/stream shutdown. PR 4 then
compares sync shared40 with async shared40, followed by async shared40 versus
normal10/recount30 with other parameters fixed. Pool partitioning is separate
from writer scheduling. Preserve synchronous sizing and #826's gate reference;
controlled long measurements, a recommendation and the implementation-issue
revision remain outstanding. This scheduler does not complete #832's comparison.
