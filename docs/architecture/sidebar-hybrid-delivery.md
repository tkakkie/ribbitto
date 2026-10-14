# Hybrid sidebar delivery verification (#961, part 2c-b1)

The external realtime lifecycle harness connects the
[coverage guard](sidebar-hybrid-coverage.md) to `Hub.Register`, `Stream.Run`,
`RecountScheduler`, an actual `unread.Changes` watch and `Sender.Send`.
It reuses the cost writer's scoped authorization/render/send probe and persisted
conversation fixtures. Recounts use the hybrid full-model adapter; only the writer
installs model witnesses. Payloads are test tokens, not browser HTML.

Computation and render gates hold work across delivery boundaries. `synctest`
advances virtual deadlines without sleeps. Assertions distinguish another
audience's denial (skip and advance) from bound-member revocation (stop before
advancing), require fresh bound authorization immediately before each send,
preserve durable order and id-free sidebar frames, and suppress pending output
after render or completion revocation.

Disconnect cancels the held computation context and releases both the member
watch and hub registration before the uncooperative computation returns.
Reconnect starts request/watch generations anew, takes a fresh full snapshot,
and sends while old work is still held. Releasing that old work cannot send or
pollute the new stream; the coverage guard also rejects its old incarnation.

Part [2c-b2](sidebar-hybrid-progress.md) reuses this harness for continuous-posting
recount/repair progress and full-model equality at pinned persisted boundaries.
Mutation evidence belongs in the PR body. Production code, `SidebarInterval` and
#865's PR 3 plan are unchanged; measurement and adoption remain #952. Together
with 2a–2c-b2, the tests cover #961's acceptance conditions.

## Matched cost-harness support (#952a1)

`costModelConnection` supplies both comparison arms with the same uncapped full
model, ordered lists, selection, display counts and badges. Sequence coverage is
read in that model's repeatable-read snapshot. Its test-token sink installs state
only after successful delivery and rejects duplicate completions. Application
snapshot SQL is counted through the supplied pool. The optional checkpoint runs
the independent oracle in that same transaction for deterministic verification
only; measured callers must pass nil so oracle work stays out of application
totals. The historical stage-1 source stays separate. Runner integration, repairs
and new accounting remain #952b–952c; the measured report and recommendation are
#952d.

The baseline `costWriter` keeps its fair scheduler and now subscribes to scoped
member watches before recounting, captures generations when queued work starts,
retains publications during computation and releases watches on every exit.
Both schedulers pass the same deterministic full-model/read-only, scope and late
completion checks. No measurement ran.

Part #952a2 adds a shared open-loop driver offering 100 changed reads/s across
5,000 streams. Before connecting, persisted seed capacity covers every member
round for the whole window, independently of posting. The production feed writer
and detached commit distinguish committed message changes from successful no-ops;
unknown commits remain failures even when they publish a recount request. Global
and per-member admission misses, no-ops and failures stay separate, without
retries. Cancellation joins admitted workers and counts unadmitted work as missed.
After writes stop, drain verification requires each stream's exact full model
against independent persisted truth captured outside application SQL totals.
Measured snapshots still pass a nil oracle checkpoint. Runner wiring remains
#952b–952c; these are deterministic checks, with no measurement.

Part #952b1 adds `costHybridConnection`, using the shared full-model source and
writer-side coverage guard. The production-only route reconciles every bound
organisation event (including unknown kinds and other channels) and member read;
it infers no hypothetical effects. A private per-connection watch adds fixed
30 s repairs with separate 10/60 s sensitivity, without publishing repairs to
other tabs or the baseline. Exit joins its timer worker and releases the member
watch. Coverage rejection preserves delivered state and requests fresh repair;
a failed send never installs the candidate. The matched pinned-model tests and
all #961 lifecycle scenarios also run through this adapter. Runner integration
is #952b2; accounting, freshness and gates remain #952c. No measurement ran.
