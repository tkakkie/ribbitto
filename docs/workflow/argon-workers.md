# Isolated implementer workers

The launcher isolates two implementers by home and worktree.
**Two workers run in production since 2026-10-24.** A third worker, each
new agy version's trust per worker, and launcher changes need the maintainer's
approval. See [operation](argon-workers-operation.md). The existing
[boundary](argon-implementer.md#the-boundary),
[run sequence](argon-implement-run.md) and
[recovery procedure](argon-implement-run-recovery.md#manual-recovery) still apply.

## Layout

Use dedicated homes `<worker-home>` for worker 1, `<worker-home-2>` for worker 2,
and `<review-home>` for reviews. The host wrapper sets the real paths.
Each worktree, `<repo-root>/wt/<name>`, must be a direct child of `<repo-root>/wt/`,
with its own branch and saved Git file. Never reuse a
worktree between live runs. Only wrappers enforce the direct-child rule;
Go checks given paths against peer homes.

| Per worker or run | Shared by the workers |
|---|---|
| Dedicated HOME; sign-in state and home configuration | Calling account and its shared settings baseline |
| Guard directory, home lock, recovery marker, guard worktree file | Pinned trusted launcher and trusted source revision |
| Trusted settings, hooks, agent and builtin hash list copies | Required common-content hashes, checked against worker 1 |
| Brain, installed agent, generated hooks and MCP configuration | agy executable and its update lifecycle |
| Worktree, branch, saved Git file, prompt, log directory | Verification image and cache infrastructure |
| Random TMPDIR beside that run's worktree | Container sweep namespace for the calling uid |

Hooks select their home's guard, so their bytes may differ between homes.
The marker names the explicit worktree; TMPDIR is created beside it. Logs
and saved Git files stay outside writable homes and worktrees under the
[run argument rules](argon-implement-run.md#arguments).

Each verify call names its container `argonreview-check-<random>` (32 lowercase
hex digits), labelled `argonreview.owner=verify`, with no worker identifier.
It stops and force-removes that container, confirms removal, then removes its
private source copy; failed cleanup disqualifies verification. SIGKILL can
leave leftovers for the sweep.

Host wrappers record a `worker` file in each run directory for attribution.
Keep it outside Go's log directory: pre-existing log directories must be empty.

## Peer homes and worktree lock

Both `implement-run` and `implement-check` accept optional repeatable
`--peer-home <abs>`, up to three values. Each must be an absolute, clean path
to an existing real directory, without symlink components or leaf. Root and
the selected home (including aliases of its directory identity) are refused
as usage errors (exit 2). Other flags may occur only once; positional arguments
are refused. Repeated peers count toward the cap but fail (exit 1).
Peers must not equal or contain one another; the same device/inode comparison
refuses overlaps (exit 1).

Before creating logs, TMPDIR or placing files, the run exits 1 if any of
these equals or lies inside a peer home: selected home, worktree, existing
log directory or its parent, saved Git file, prompt file or agy executable.
It also refuses peers inside the worktree or selected home. Containment
checks walk parent chains, comparing device/inode identities, not prefixes.
`implement-check` applies the same rules to its home, worktree, saved Git
file and agy path; it has no prompt or log arguments. Peer validation neither
inspects peer configuration or markers nor acquires peer home locks. With
peers, standalone checks refuse symlinked agy leaves or components, matching
the run's regular-file path rule.

The run passes every peer to its initial at-rest, pre-, post- and final
at-rest checks in process. Peers do not change the guard or trusted-build
contract. Wrappers must supply them on standalone checks too. Without peers,
validation adds no checks or output; the worktree lock protects independent
homes too.

After taking the home lock, the runner opens the worktree read-only with
O_DIRECTORY, O_NOFOLLOW and O_CLOEXEC and takes an exclusive, nonblocking
flock. Another holder causes exit 1 before log/TMPDIR creation or placement.
The locked descriptor must match the path's lstat; swaps or symlinks are
refused. Only the persistent home lock file may have been created; nothing
is created in the worktree. If that file existed, refusal changes no files.
Both locks last through cleanup and status delivery, then close. agy and
its children do not inherit the worktree descriptor. Worktree locks are
independent, even with the same launcher.

Crashes release locks but may leave agy alive. Markers require ordered
recovery before home or worktree reuse; released locks never imply a free
worktree. Cross-worker recovery below is host policy, NOT launcher-enforced.

## Host wrapper contract

Review each worker's run and check wrapper changes before making them.
Hard-code HOME and PEERS; accept no caller-supplied home,
peer list or worker identity. Use these exact peer sets:

| Wrapper | HOME | PEERS |
|---|---|---|
| Worker 1 | `<worker-home>` | `<worker-home-2>`, `<review-home>` |
| Worker 2 | `<worker-home-2>` | `<worker-home>`, `<review-home>` |

Peers must exist before wrappers name them; existence authorizes neither
sign-in nor execution. Review runs retain the
[review boundary](running-argon.md#the-boundary).

Before every run, the host wrapper / Claude lists every worker's guard/
for implement-run.recovery-needed and reads guard/implementer-worktree to
identify the affected worktree. No worker may receive a worktree whose last
run left a marker or whose stop is unconfirmed until ordered recovery,
including confirmed session stop, completes. Missing or uncertain
attribution requires recovery before assignment. This is host policy;
launcher refusal for a peer marker naming this worktree is deferred hardening.

Both wrappers read worker 1's implementer/verify-mode; never copy it to an
editable worker 2 file. Idle checks select off; active checks and runs use
the trusted mode. Pin launcher hash and trusted source per the
[switch record](argon-implement-switch.md#wrappers-and-trusted-inputs).
Updated binaries need regenerated home-specific trusted hooks; never bypass
byte comparison to accept mismatched configuration.

Before launch, require SHA-256 equality with worker 1 for common files in
each home's implementer directory: settings.json, trusted agent argon-implementer.md
and trusted builtin.agy-<v>.txt for the selected agy version. Wrappers check
this; Go independently checks each home's trusted copies and baseline,
without reading worker 1's copies. Hooks, guard worktree files and sign-in
state are home-specific. Each worker's sign-in and builtin trust need separate
maintainer decisions even with equal bytes.

Require a direct child of `<repo-root>/wt/`; pass its absolute path.
In a fresh run directory, record worker identity and give Go an empty child
log directory. Keep prompts and saved Git files outside all
peer homes and writable trees. Never infer peers from existing directories
or allow environment overrides to drop the review home.

## N workers

Each worker needs its own home, guard directory, lock, recovery marker and
worktree at `<repo-root>/wt/<name>`. For N>=3, use a shared run.zsh with a worker
argument and fixed table; there is no --worker flag in
Go, which would compile host paths into the launcher.
Each worker's run and check wrappers must list ALL other workers' homes as
`--peer-home`, plus the review home as above. The three-peer cap counts the
review home: N−1+1 ≤ 3, so at most three workers fit this contract. Four or
more need a cap change and a new review; do not drop the review peer.

Every worker uses shared read-only content and the SHA-256 equality rule
against worker 1's master copies. All read worker 1's verify-mode.
Before assigning any worktree, check all N homes for recovery markers:
no worker receives a worktree whose last run left a marker until recovery.
The recovery and uncertain-attribution rules above still apply.

container-sweep stays uid-global with its liveness protections. Coupling
grows with N: one worker's sweep can reclaim another's dead leftovers
(design note #1577, R4/R5). Reconsider deferred per-worker container tags
(option S1) at N>2. Quota and Docker capacity remain unknowns deciding
whether N>2 is useful (design note E1/E2); experiments require the
maintainer's explicit start. Going beyond two workers needs separate
maintainer approval.

## Operation and rollback

The container sweep is uid-global, not per-worker. A held copy
lock, a matching live non-orphan Docker client, or age under 90 seconds each
protects a container, including a running worker's. It can reclaim either
worker's dead leftovers, subject to the
[sweep validation and mount checks](argon-verify-sweep.md), without per-worker
ownership or reclamation guarantees. Per-worker container tags remain deferred.
After an abnormal end, run
`container-sweep --dry-run` before the next start of either worker, inspect
the result, then perform ordered manual recovery, one at a time.
A dry run does not replace independently confirming session stop or
the at-rest check, and a released worktree lock is not evidence of stop.

The shared agy executable can auto-update during a run; the launcher does
not freeze it. Version/builtin checks fail closed if the new version has no
matching trusted list or observed builtin bytes differ. A failure after
start retains the recovery marker. Coordinate updates and approvals for
both workers' trusted lists.

To disable worker 2, remove the execute bit from its run.zsh wrapper. This
does not stop an active session. Stop and recover active or abnormal runs
before further changes; do not remove a lock or marker to permit a start.

If the new launcher binary must also be removed, first disable worker 2,
then stop and recover both workers. Never run mixed launchers: `c00020d`
has no `--peer-home` or worktree lock, so rolled-back worker 1 beside a running
worker 2 has no peer or worktree-lock protection.
Restore worker 1's complete *.c00020d backup set of hooks.json, run.zsh and
checks.zsh, including the home's installed off hooks. See
[operation](argon-workers-operation.md#rollback) for hashes.
Keep it unchanged and verify it against the recorded hashes before a rollback.
Restore the pinned binary/source selection with the wrappers. To complete
rollback, run the old checks.zsh idle check and one real run with maintainer
approval. A rollback drill with a real run was NOT done.
Never mix in the [older set](argon-implement-switch.md#recovery-and-rollback).
Removing peers does not restore a consistent trusted set.
