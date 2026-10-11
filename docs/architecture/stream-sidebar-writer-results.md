# Sidebar single-writer prototype (#832, PR 3)

Part of #832, based on `codex/recount-queue` (PR #838), above PR #836.
This is delivery-safety evidence in realtime's external test package only.
Production `Stream.Run`, wiring, queries and `realtime.SidebarInterval` are
unchanged. The [scheduler contract](stream-sidebar-queue-results.md) and
[pool protocol](stream-sidebar-pool-results.md) remain separate evidence.
No performance comparison, recommendation or production configuration is complete.
PR 4 adds comparison arms and queue telemetry; the maintainer starts measurements.

## Prototype contract

`internal/realtime/cost_writer_test.go` composes the PR 2 scheduler with exported
stream interfaces. The fixture registers exactly one scheduler slot per writer;
the writer owns cancellation on every exit. Workers only call `Sidebar.Recount`
and publish a versioned payload/error. Only the writer renders durable events,
authorizes frames, sends and advances the durable cursor. No worker sends.

The writer drains ordered event batches while its own recount is queued or
running. It consumes completions between batches and in its idle select, so
neither a durable backlog nor absence of a durable wake strands a completion.
One joined bridge per writer adapts exported `Hub.Wait` to bounded selectable
wakes. Its cursor advances independently; the event reader remains the truth.
Exit cancels/joins the bridge and cancels its scheduler registration. As PR 2
specifies, uncooperative computations retain their slots until return and their
late completions are suppressed.

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

Heartbeats use time since the last successful write, progress while recounts
wait and re-check the same membership. A deny ends the writer even while idle.
Every exit cancels pending/running work, drains queued completions through the
scheduler and prevents any late frame. Send/recount/read/render errors stop the
loop; errors from superseded results are discarded with their obsolete payload.
Durable output preserves rendered message IDs and order; successful or skipped
durable events advance the cursor. Sidebar output is always `Name="sidebar"`,
`ID=0`, `Ephemeral=true`, irrespective of the source envelope. These are the
sender flags that omit SSE `id:`; the prototype does not implement wire encoding.
It uses no first-unread value or counting implementation, including #861's change.

## Deterministic evidence

`internal/realtime/cost_writer_safety_test.go` uses channel gates and
`testing/synctest`. Fake-time sleeps exercise interval/heartbeat deadlines only;
`Wait` and channel gates synchronize work. Atomic revocation/send witnesses also stay safe under mutations.

- Progress gates another stream to prove queued progress, then this stream to
  prove running progress. Both deliver IDs 1,2,3 before releasing any recount.
- NoRollback rejects an invalidated completion, proves the minimum interval and
  sends fresh state without a durable wake. Injected older/duplicate completions
  independently exercise the writer guard beyond scheduler serialization.
- Stops holds an uncooperative recount and pending successor. Disconnect,
  cancellation/revocation during render, sidebar revocation and idle heartbeat
  revocation return the expected cause and unchanged cursor. Late work adds no
  frame or retained registration/completion. An allowed heartbeat precedes deny.
- The sender asserts the immediately preceding operation was the bound-member
  check; the authorizer checks account, slug, organization and audience scope.

## Mutation commands and observed assertions

Export the worktree-local caches from the
[pool protocol](stream-sidebar-pool-results.md#reproduce),
`RIBBITTO_TEST_DATABASE_URL=<local test database>` and `RIBBITTO_REQUIRE_DB=1`.
The restored normal suite passes:

```sh
go test -race -count=1 -run '^TestCost' -timeout 2m ./internal/realtime
```

The 22 isolated mutations below each exited 1 at an assertion, without compile
failure, race, panic or timeout, then were restored. Existing tests are unchanged.
Table keys name these exact commands:

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

Initial trials exposed a cleanup deadlock in a mutation without cancellation
and a fixture observation race. The final blocking mutation retains cancellation;
atomic witnesses and cancellation before frame inspection remove the race.
Those initial runs are excluded from final mutation evidence.

## Production gaps and handoff

This loop is a scheduling experiment for ordered channel messages, not a port
of `Stream.Run`. Real integration must retain subscription kind/topic and
rendered-topic filtering, organization-scoped triggers, replay bounds/gap resets,
negative-cursor validation, configurable batches and all cursor/error rules.
Only organization/channel/message-interest filtering is modeled here. It must
integrate presence/typing owners and generations, member sidebar watches
subscribed before initial recount, captured/covered watch generations and release.
The wake bridge must be replaced by the production combined wait, incorporating
completion, heartbeat and recount deadlines without lost wakes or unbounded
helper goroutines. Reusable timers, backlog heartbeat behavior, loader limits,
write deadlines and slow-client behavior need production verification.

Admission/capacity refusal, context propagation, process shutdown joining,
registration ownership and member/account fairness need composition-root design.
The fixture supplies the slot; production must prevent duplicate registrations
and startup leaks. Item bounds do not bound payload bytes. Supersession's
freshness cost and error policy need validation. These tests establish no
HTTP/HTML, count-query, browser or multi-member workload result. Existing tests
remain; Claude cross-review and tier A / Grok await the external orchestrator.
The local-only handoff supersedes Status #1/log #2 operations; no GitHub, other
AI CLI, browser, commit or push was used.

PR 4 compares sync shared40 with async shared40, then async shared40 with normal10
plus recount30, changing one variable per comparison. It adds queue/backlog and
aggregate pool telemetry; growing backlog disqualifies. Controlled overnight
measurements, connection-budget inputs and the implementation-issue recommendation
remain outstanding under #832.
