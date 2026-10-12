# Sidebar single-writer prototype (#832, PR 3)

Part of #832, based on PR #838 above PR #836; test-only, production unchanged.
See the [scheduler](stream-sidebar-queue-results.md) and
[pool protocol](stream-sidebar-pool-results.md). PR 4 adds comparisons/telemetry.
[Corrected measurements](stream-sidebar-arms-measured.md) are complete after the
liveness fix below; the candidate needs an advance stability criterion and
production-wiring validation before recommendation.

## Prototype contract

`internal/realtime/cost_writer_test.go` composes the PR 2 scheduler: one slot per
writer, canceled on exit. Workers publish versioned recounts/errors; only the
writer renders events, authorizes, sends and advances the cursor.

Ordered event batches progress during queued/running recounts. Completions are
consumed between batches and in the idle select, avoiding backlog/quiet starvation.
A joined `Hub.Wait` bridge supplies bounded wakes with an independent cursor;
the event reader remains the truth. Exit cancels/joins the bridge and cancels
registration. Uncooperative computations retain slots until return (PR 2).

Connect starts dirty. Each durable recount trigger increments the desired
version, including triggers outside message delivery interests. One outstanding
request retains later invalidations in a dirty flag. A completion newer than
the last sent frame sends its own payload even if the desired version has moved
on. After sending, `version > result.version` keeps the writer dirty for the
next interval. Older/duplicate results are dropped before authorization.
The minimum `realtime.SidebarInterval` starts on consuming each attempt, including
discarded results; delayed consumption is conservative. Triggers never move it.
This allows progress under continuous invalidation without rollback, but does
not guarantee a snapshot is current at send.

The pre-fix ladder computed many recounts but sent few at 5,000 streams,
consistent with desired-version-equality starvation. Samples measure oldest
pending age, not completion wait; freshness does not locate individual sends.
[Controlled results](stream-sidebar-arms-measured.md#writer-starvation-found-by-the-ladder)
give pre/post-fix ranges by rate/arm. Tests below establish liveness separately.

Every send retains #859: render/compute, authorize account/slug/own member, send.
Another audience is checked first; its deny skips. Bound-member deny returns
`realtime.ErrMembershipRevoked` without advancing past that event. Authorization
errors stop delivery; cancellation wins over deny. Revocation after the check's
snapshot retains production's unavoidable window.

Heartbeats follow time since last write, progress during recounts and re-check
membership; deny ends idle writers too. Exit cancels work and drains completions,
preventing late frames. Send/recount/read/render errors stop delivery; errors at
versions no newer than sent are discarded. Durable IDs/order are preserved;
successful/skipped events advance the cursor. Sidebar flags always become
`Name="sidebar"`, `ID=0`, `Ephemeral=true` (no SSE `id:`); wire encoding is unmodeled.

## Deterministic evidence

`internal/realtime/cost_writer_safety_test.go` uses channel gates and
`testing/synctest`. Fake-time sleeps exercise interval/heartbeat deadlines only;
`Wait`/gates synchronize work; atomic revocation/send witnesses tolerate mutations.

- Progress gates queued/running work; both deliver IDs 1,2,3 before any recount.
- BacklogCompletion gates render 100; a recount completes there and sends before
  IDs 101–103, preserving order.
- RecountProgress gates three consecutive recounts, invalidating each while
  queued and running. Each sends its own payload; later invalidations request
  another recount exactly at the next minimum interval, without a new wake.
- NoRollback sends invalidated/newer state, proves the minimum interval and
  progress without a durable wake. Injected older/duplicate completions test
  the writer guard beyond scheduler serialization.
- Stops gates an uncooperative recount and successor. Disconnect, render/sidebar
  and heartbeat revocation return the expected cause/cursor; late work leaves no
  frames/registration/completion. An allowed heartbeat precedes deny.
- The sender requires bound-member authorization immediately before sending;
  the authorizer checks scope. Audiences covers own/other members and broadcasts,
  skipped-event cursor advancement and subsequent progress.

## Mutation commands and observed assertions

Export the worktree-local caches from the
[pool protocol](stream-sidebar-pool-results.md#reproduce),
`RIBBITTO_TEST_DATABASE_URL=<local test database>` and `RIBBITTO_REQUIRE_DB=1`.
The restored normal suite passes:

```sh
go test -race -count=3 -run '^TestCost' ./internal/realtime
```

The historical 22 original/three follow-up mutations failed assertions and were
restored, without compile failure, race, panic or timeout. The former
desired-version-equality mutation (`stale completion was sent`) is superseded.

Historical command form (substitute the suffix mapped below for `$case`):

```sh
go test -race -count=1 -run "^TestCostWriter${case}$" -timeout 10s ./internal/realtime
```

Q=`Progress/true`, R=`Progress/false`, P=`Progress`, N=`NoRollback`,
H=`Stops/heartbeat`, D=`Stops/durable`, S=`Stops`, C=`Stops/render-disconnect`,
X=`Stops/disconnect`, B=`Stops/sidebar`, L=`BacklogCompletion`,
O=`Audiences/other`, M=`Audiences/own`.

| Command | Deliberate defect | Actual failing assertion |
|---|---|---|
| Q | Wait for completion at admission (cancellation still unblocks) | `durable delivery stopped behind recount: []` |
| R | Same blocking wait with own computation running | `durable delivery stopped behind recount: []` |
| P | Increment rendered message ID before send | `durable IDs/order changed`, IDs 2,3,4 |
| P | Reverse event iteration | `durable IDs/order changed`, IDs 3,2,1 |
| P | Set durable Ephemeral=true | `durable IDs/order changed`, ephemeral IDs 1,2,3 |
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

Initial deadlocked/racy trials are excluded; final trials retain cancellation
and use atomic witnesses/cancellation before inspection.

The liveness correction changes NoRollback's first assertion from one durable
frame only (`stale completion was sent`) to two frames, including completion
payload `1` with sidebar flags. The later payload `2` assertion moves from
index 1/count 2 to index 2/count 3; older/duplicate checks retain count 3.
The interval, payload/flags and rollback checks remain.

Each new mutation below was applied alone and restored, exiting 1 at the shown
assertion without compile failure, race, panic or timeout. Commands:

```sh
# RP: repeated recount progress; N: no rollback
go test -race -count=1 -run '^TestCostWriterRecountProgress$' -timeout 10s ./internal/realtime
go test -race -count=1 -run '^TestCostWriterNoRollback$' -timeout 10s ./internal/realtime
```

| Command | Deliberate defect | Actual failing assertion |
|---|---|---|
| RP | Restore `result.version == version && result.version > sent` | `recount 1 completion made no sidebar progress: got 0 frames, want 1` |
| RP | Set dirty to false after sending | `recount 2 was not queued at next interval after outdated frame` |
| N | Use `result.version >= sent` | `completed recount rolled back or duplicated a newer frame` (duplicate version 2) |
| N | Use `result.version > 0 && sent >= 0` | Same assertion (older version 1) |
| RP | Replace completion data with `[]byte("wrong")` | `recount 1 frame did not preserve completion payload/flags`, Data=`[119 114 111 110 103]` |

## Production gaps

This is not a port of `Stream.Run`: only organization/channel/message-interest
filtering is modeled. Integration must retain kind/topic/rendered-topic filtering,
organization-scoped triggers, replay bounds/gap resets, negative-cursor validation,
configurable batches and cursor/error rules. It needs presence/typing owners and
generations, member watches subscribed before initial recount, captured/covered
watch generations and release. Replace the bridge with production's combined wait
for completion/heartbeat/recount deadlines without lost wakes or unbounded helpers.
Verify reusable timers, backlog heartbeats, loaders, write deadlines/slow clients.

Wiring needs admission/capacity refusal, context propagation, shutdown joining,
registration ownership and member/account fairness, without duplicate slots or
startup leaks. Item bounds do not bound bytes. Validate snapshot lag/error policy.
These tests establish no HTTP/HTML, count-query, browser or multi-member results.

PR 4's completed comparisons isolate scheduling and pool partitioning; growth
disqualifies. The [candidate](stream-sidebar-arms-measured.md) still needs
production budgets, an advance stability criterion and real-wiring validation.
