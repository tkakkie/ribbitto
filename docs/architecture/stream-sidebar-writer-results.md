# Sidebar single-writer prototype (#832, PR 3)

Part of #832, based on `codex/recount-queue` (PR #838), above PR #836.
Delivery-safety evidence in realtime's external test package only; production is
unchanged. The [scheduler contract](stream-sidebar-queue-results.md) and
[pool protocol](stream-sidebar-pool-results.md) are separate evidence.
PR 4 adds performance comparisons and queue telemetry; measurements, a
recommendation and production configuration remain outstanding.

## Prototype contract

`internal/realtime/cost_writer_test.go` composes the PR 2 scheduler with exported
stream interfaces: one slot per writer, canceled on every exit. Workers recount
and publish versioned payloads/errors; only the writer renders durable events,
authorizes, sends and advances the cursor.

The writer drains ordered event batches while its own recount is queued or
running. It consumes completions between batches and in its idle select, so
neither a durable backlog nor absence of a durable wake strands a completion.
A joined bridge adapts `Hub.Wait` to bounded selectable wakes with an independent
cursor; the event reader remains the truth. Exit cancels/joins it and cancels the
registration. Uncooperative computations retain slots until return; late
completions are suppressed (PR 2).

Connect starts dirty. Each durable recount trigger increments the desired
version, including triggers outside message delivery interests. One outstanding request retains later invalidations in a dirty flag.
Only the desired version above the last sent version can send. Dropping a
superseded or duplicate result occurs before authorization; newer work stays dirty. A fixed minimum of
`realtime.SidebarInterval` starts when the writer consumes each completed attempt,
including a discarded result; it is conservative if consumption was delayed.
Triggers never move that deadline. Sustained invalidation can repeatedly discard
snapshots; freshness and backlog under load are unmeasured and remain PR 4 work.

Every send retains #859's rule: render/compute first, authorize the stream's
account, slug and own member last, then send. Another audience is checked first;
its deny skips. A bound-member deny returns `realtime.ErrMembershipRevoked`
without advancing past that event. Authorization errors stop delivery;
cancellation wins over deny and prevents send. Revocation after the check's
snapshot retains production's unavoidable window.

Heartbeats measure time since the last write, progress during recounts and
re-check membership; denial ends idle writers too. Exit cancels pending/running
work and drains completions through the scheduler, preventing late frames.
Send/recount/read/render errors stop delivery; superseded errors are discarded.
Durable output preserves rendered message IDs and order; successful or skipped
durable events advance the cursor. Sidebar output is always `Name="sidebar"`,
`ID=0`, `Ephemeral=true`, irrespective of the source envelope: flags that omit
SSE `id:`. Wire encoding is not modeled.

## Deterministic evidence

`internal/realtime/cost_writer_safety_test.go` uses channel gates and
`testing/synctest`. Fake-time sleeps exercise interval/heartbeat deadlines only;
`Wait` and channel gates synchronize work. Atomic revocation/send witnesses also stay safe under mutations.

- Progress gates another stream to prove queued progress, then this stream to
  prove running progress. Both deliver IDs 1,2,3 before releasing any recount.
- BacklogCompletion gates the last render in a 100-event batch. A real recount
  completes there; its sidebar sends before IDs 101–103, preserving durable order.
- NoRollback rejects an invalidated completion, proves the minimum interval and
  sends fresh state without a durable wake. Injected older/duplicate completions
  independently exercise the writer guard beyond scheduler serialization.
- Stops holds an uncooperative recount and pending successor. Disconnect,
  cancellation/revocation during render, sidebar revocation and idle heartbeat
  revocation return the expected cause and unchanged cursor. Late work adds no
  frame or retained registration/completion. An allowed heartbeat precedes deny.
- The sender asserts the immediately preceding operation was the bound-member
  check; the authorizer checks account, slug, organization and audience scope.
  Audiences covers own-member (bound only), other-member (deny, no send, cursor
  advances) and organization-wide delivery. A later broadcast verifies progress.

## Mutation commands and observed assertions

Export the worktree-local caches from the
[pool protocol](stream-sidebar-pool-results.md#reproduce),
`RIBBITTO_TEST_DATABASE_URL=<local test database>` and `RIBBITTO_REQUIRE_DB=1`.
The restored normal suite passes:

```sh
go test -race -count=1 -run '^TestCost' -timeout 2m ./internal/realtime
```

The 22 original and three follow-up mutations exited 1 at assertions, without
compile failure, race, panic or timeout, then were restored. Existing tests remain.
Table keys name the commands:

```sh
# Q: queued; R: running; P: both progress cases
go test -race -count=1 -run '^TestCostWriterProgress/true$' -timeout 10s ./internal/realtime
go test -race -count=1 -run '^TestCostWriterProgress/false$' -timeout 10s ./internal/realtime
go test -race -count=1 -run '^TestCostWriterProgress$' -timeout 10s ./internal/realtime
# N: no rollback; H: heartbeat; D: durable revocation; S: all stop cases
go test -race -count=1 -run '^TestCostWriterNoRollback$' -timeout 10s ./internal/realtime
go test -race -count=1 -run '^TestCostWriterStops/heartbeat$' -timeout 10s ./internal/realtime
go test -race -count=1 -run '^TestCostWriterStops/durable$' -timeout 10s ./internal/realtime
go test -race -count=1 -run '^TestCostWriterStops$' -timeout 10s ./internal/realtime
# C: render cancellation; X: idle disconnect; B: sidebar revocation
go test -race -count=1 -run '^TestCostWriterStops/render-disconnect$' -timeout 10s ./internal/realtime
go test -race -count=1 -run '^TestCostWriterStops/disconnect$' -timeout 10s ./internal/realtime
go test -race -count=1 -run '^TestCostWriterStops/sidebar$' -timeout 10s ./internal/realtime
# L: backlog completion; O: other audience; M: own member
go test -race -count=1 -run '^TestCostWriterBacklogCompletion$' -timeout 10s ./internal/realtime
go test -race -count=1 -run '^TestCostWriterAudiences/other$' -timeout 10s ./internal/realtime
go test -race -count=1 -run '^TestCostWriterAudiences/own$' -timeout 10s ./internal/realtime
```

| Command | Deliberate defect | Actual failing assertion |
|---|---|---|
| Q | Wait for completion at admission (cancellation still unblocks) | `durable delivery stopped behind recount: []` |
| R | Same blocking wait with own computation running | `durable delivery stopped behind recount: []` |
| P | Increment rendered message ID before send | `durable IDs/order changed`, IDs 2,3,4 |
| P | Reverse event iteration | `durable IDs/order changed`, IDs 3,2,1 |
| P | Set durable Ephemeral=true | `durable IDs/order changed`, ephemeral IDs 1,2,3 |
| N | Remove desired-version equality | `stale completion was sent` |
| N | Remove last-sent guard (retain variable with a true expression) | `completed recount rolled back or duplicated a newer frame` (duplicate) |
| N | Bypass version guards once sent>0 | Same assertion (older completion after newer frame) |
| N | Set next deadline to now | `recount started before minimum interval` |
| N | Trust source ID/name/Ephemeral | `quiet completion lost or sidebar carried durable ID`, ID 99, untrusted, false |
| P | Omit durable bound check | `send was not immediately preceded by bound-member authorization` |
| N | Omit sidebar bound check | Same pre-send assertion |
| H | Omit heartbeat bound check | Same pre-send assertion; `revoked or disconnected writer kept running` |
| D | Bind to a different member | `revoked or disconnected writer kept running`; ended writer sent a message |
| S | Ignore bound-member deny | `revoked or disconnected writer kept running`; ended writer sent message/sidebar |
| C | Remove post-authorization context check | `writer did not stop without cursor advance`, cursor=1; ended writer sent a message |
| S | Omit scheduler registration cancellation | `ended writer sent or retained late recount`, registered=1 |
| D | Return denied event's sequence as cursor | `writer did not stop without cursor advance`, cursor=1 |
| X | Disable disconnect case in idle select | `revoked or disconnected writer kept running` |
| N | Disable completion case in idle select | `quiet completion lost or sidebar carried durable ID` |
| D | Move authorization before render | Pre-send assertion; `revoked or disconnected writer kept running` |
| B | Authorize in compute closure, omit writer check | `revoked or disconnected writer kept running`; ended writer sent sidebar |
| L | Remove the `completed == nil` mailbox-consumption block | `recount completion was not sent between batches before backlog drained` |
| O | Send when `allowed || event.AudienceMemberID != nil` | `audience other sent 1 frames, want 0` |
| M | Bound-check only nil or other-member audiences | `send was not immediately preceded by bound-member authorization` |

Initial deadlocked/racy trials are excluded: the final blocking mutation retains
cancellation; atomic witnesses and cancellation before inspection remove the race.

## Production gaps

This ordered-message scheduling experiment is not a port of `Stream.Run`.
Integration must retain subscription kind/topic and
rendered-topic filtering, organization-scoped triggers, replay bounds/gap resets,
negative-cursor validation, configurable batches and all cursor/error rules.
Only organization/channel/message-interest filtering is modeled here. It must
integrate presence/typing owners and generations, member sidebar watches
subscribed before initial recount, captured/covered watch generations and release.
The wake bridge must be replaced by the production combined wait, incorporating
completion, heartbeat and recount deadlines without lost wakes or unbounded
helper goroutines. Reusable timers, backlog heartbeat behavior, loader limits,
write deadlines and slow-client behavior need production verification.

Composition-root design must cover admission/capacity refusal, context propagation,
shutdown joining, registration ownership and member/account fairness. Production
must prevent duplicate slots and startup leaks. Item bounds do not bound bytes.
Supersession's freshness cost and error policy need validation. These tests
establish no HTTP/HTML, count-query, browser or multi-member workload result.

PR 4 compares sync shared40 with async shared40, then async shared40 with normal10
plus recount30, changing one variable per comparison. It adds queue/backlog and
aggregate pool telemetry; growing backlog disqualifies. Controlled overnight
measurements, connection-budget inputs and the implementation-issue recommendation
remain outstanding under #832.
