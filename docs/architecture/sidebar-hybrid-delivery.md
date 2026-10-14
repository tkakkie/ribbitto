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
watch. Both `repair` and `ignored` coverage rejections preserve the installed
model and request fresh private repair after the recount throttle;
a failed send never installs the candidate. The matched pinned-model tests and
all #961 lifecycle scenarios also run through this adapter. Runner integration
is #952b2; accounting, freshness and gates remain #952c. No measurement ran.

Rejected completions return nil, so the production stream records them as
delivered, advances its covered generation and resets the heartbeat timer even
though no frame was installed. The observer's `Delivered` count and heartbeat
timing are therefore not hybrid-arm evidence. Hybrid evidence uses sender-side
probe frames, the adapter's separate `repair`/`ignored` rejection counts and SQL
totals: a rejected computation's SQL stays counted. Measured runs must keep
heartbeats off, as the cost runner already does. The private wake requests a
recount after the throttle; it cannot undo the stream's delivery bookkeeping.
On a single production stream, increasing request versions and one incarnation
prevent `ignored`; its wake remains defensive. Virtual-time regressions force
stale coverage through `Stream.Run` and a duplicate directly through the arm's
`Send`, then require the stream's next recount to install the changed model.

A production hybrid requires a sidebar-only retry outcome on the stream, such
as a retry sentinel: keep the stream dirty, preserve sent version, covered
generation and last-written time, retain the recount throttle and record a
separate observer outcome. This is a prerequisite for adoption, not implemented
here: #952 excludes production package changes.

Part #952b2 wires `baseline` and `hybrid` into the cost runner with the same
full-model source, member fixtures, caches and pool 36/26-worker budget. Baseline
keeps `costWriter`/`costQueue`; hybrid uses `RecountScheduler`. Both seed the shared
100 changed-read/s plan before connecting, run it alongside posts and join all
writes before capturing independent final truth through the fixture pool. Drain
requires every delivered full model to match, including read-only changes with
unchanged sequence coverage. Application snapshots pass nil oracle checkpoints.
Sender-side frames and separate rejection counts remain available for #952c;
these arms bypass historical verdict output until its accounting/freshness/gates
land. Deterministic wiring/drain tests ran; no measurement ran.

Part #952c1 separately records sidebar recount SQL under the existing sidebar
role, remaining application SQL (posts, reads, events and authorization), and
their checked sum per window. Both shares count attempted queries plus
BEGIN/COMMIT/ROLLBACK, including snapshot transaction controls. Actual recount
starts, successful snapshot sends and successful delta sends have separate
counts. Errors, cancellation and coverage rejection retain computation and SQL
costs; rejected completions and failed sends add no send. Window totals
subtract counters captured after initial models arrive, excluding setup; oracle
truth stays on the untraced fixture pool. At exit both schedulers join actual
computations before accounting closes, retaining cancelled work's SQL. Atomic
sender totals can be captured while writers run; the observer and heartbeat
timing provide no evidence. Production-only routing emits zero deltas: that is
reported explicitly and cannot establish delta evidence. Synthetic delta-frame
tests verify classification only, apart from production-only results. Freshness,
drift, queue telemetry and acceptance/adoption gates remain #952c2–c4; no
measurement ran.

Part #952c2a retains post and changed-read commit witnesses through pgx's
`TraceQueryEnd`: timestamp the successful COMMIT reply before `Commit` and the
use-case call return, including detached read commits. This is the client-observed
commit boundary, not the server's wall-clock instant. Failed, unknown or rolled-back
commits yield no timestamp; successful no-op reads yield no change witness.
Both arms pin sequence and scoped feed-prefix coverage with the full model in
one repeatable-read snapshot. The feed-prefix `read_range` witness (`MAX(hi)-1`
where `lo=0`) keeps the same statement count but adds small measurement work
inside a measured statement, identical in both arms and not subtracted from any
result. It proves the runner's monotonic feed reads through a cursor, not general
topic-read coverage or a production revision.
Sender successes retain both coverage witnesses, monotonic elapsed send time and
an immutable SHA-256 digest covering scope, ordered identities/names, uncapped and
display counts, badges and selection; no historical full models are retained.
The digest is computed after capturing sender success time, with no oracle query;
952c2b can compare it with an independently expected model. Hashing is additional
witness work in both arms and is not subtracted from results. Computed, rejected
and failed output supplies no sender witness. Each stream retains its first
1,024 successes in a fixed-capacity buffer: 56 bytes per record, at most 56 KiB
plus 56 bytes for the slice, time anchor and overflow counter on 64-bit hosts
(57,400 bytes/stream, 287,000,000 bytes or about 273.7 MiB for 5,000 streams per
window, independent of subsequent sends). This excludes existing current/pending
models and the shared persisted-change log. The buffer includes setup and drain;
it accommodates the cited approximately 600 sends/stream in a 600 s window.
Further successes increment an overflow counter without growing or overwriting
evidence; 952c2b must report affected windows incomplete rather than silently
passing truncated samples. Redundant sender payload history is removed; atomic
send totals remain. Only deterministic tests clone historical models. Gated
virtual-time tests hold calls after commit and frames across committed reads and
continuing posts. Freshness samples, attribution and incomplete handling remain
#952c2b; no measurement ran.
