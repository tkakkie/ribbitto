# Isolated implementer workers

The launcher separates two implementers using distinct homes and worktrees.
**A second worker is NOT enabled in production.** Host experiments, installing
this binary, creating and signing in worker 2, trusting its builtin list, and
go-live each need the maintainer's separate approval. The existing
[boundary](argon-implementer.md#the-boundary),
[run sequence](argon-implement-run.md) and
[recovery procedure](argon-implement-run-recovery.md#manual-recovery) still apply.

## Layout

Use worker 1's dedicated home at ~/dev/ribbitto/agy-worker-home and worker 2's
at ~/dev/ribbitto/agy-worker-home-2. The review home is
~/dev/ribbitto/agy-trial-home. Each worker's worktree must be a direct child
of ~/dev/ribbitto/wt/, with its own branch and saved Git file. Do not reuse a
worktree between live runs. The direct-child rule is enforced by the host
wrappers; the Go commands accept the validated explicit worktree path.

| Per worker or run | Shared by the workers |
|---|---|
| Dedicated HOME; sign-in state and home configuration | Calling account and its shared settings baseline |
| Guard directory, home lock, recovery marker, guard worktree file | Pinned trusted launcher and trusted source revision |
| Trusted settings, hooks, agent and builtin hash list copies | Required common-content hashes, checked against worker 1 |
| Brain, installed agent, generated hooks and MCP configuration | agy executable and its update lifecycle |
| Worktree, branch, saved Git file, prompt, log directory | Verification image and cache infrastructure |
| Random TMPDIR beside that run's worktree | Container sweep namespace for the calling uid |

Hooks select their own home's guard, so different homes' hooks need not have
the same bytes. The worktree marker names the explicit worktree; TMPDIR is
created beside it. Logs and saved Git files remain external to writable homes
and worktrees under the [run argument rules](argon-implement-run.md#arguments).

There is no worker flag. Host wrappers record a `worker` file in each run
directory to attribute evidence. Keep this wrapper-owned file outside the log
directory passed to Go: a pre-existing log directory must be empty.

## Peer homes and worktree lock

Both `implement-run` and `implement-check` accept the optional repeatable flag
`--peer-home <abs>`, up to three values. Every peer must be an absolute, clean
path to an existing real directory, with no symlink component or leaf. Root
is refused. A peer equal to the selected home, including the same directory
identity under another spelling, is refused. These are usage errors (exit 2).
Every other flag remains accepted only once; positional arguments remain
refused. A peer may be repeated within the three-value cap, but wrappers
should list each peer only once.

The run refuses with exit 1 before creating logs, TMPDIR or placing files if
any of these paths equals or lies inside a peer home: the selected home,
worktree, log directory's existing parent, saved Git file, prompt file or agy
executable. It also refuses if a peer lies inside the worktree or selected
home. Comparisons walk parent chains and compare device/inode identities,
as the existing containment checks do; string prefixes alone are insufficient.
`implement-check` applies the same separation rules to its home, worktree,
saved Git file and agy path. It has no prompt or log arguments; those two
checks belong to `implement-run`. Peer validation does not inspect a peer's
configuration or marker and does not acquire its home lock.

The run passes every peer to its in-process initial at-rest, pre-, post- and
final at-rest checks. Peers do not change the guard or trusted-build contract.
The wrappers must supply them on standalone checks too. Without peers, peer
validation adds no checks or output; existing Go behavior remains the same
except for the new worktree lock, which also protects independent homes.

After taking the existing home lock, the runner opens the worktree directory
read-only with O_DIRECTORY, O_NOFOLLOW and O_CLOEXEC and takes an exclusive,
nonblocking flock on that descriptor. Another holder causes exit 1 before
log/TMPDIR creation or placement. The already acquired home lock can have
created its persistent lock file; nothing is created in the worktree. With
that home lock file already present, the refusal changes no files. Both
locks remain held through cleanup and status delivery and close at the end.
The worktree descriptor is not inherited by agy or its children. Different
worktrees have independent locks, even when both runs use the same launcher.

A crash releases locks but can leave agy alive. The marker still requires
ordered recovery before reuse of that home. A marker in worker 1's home does
not by itself refuse a run using worker 2's home.

## Host wrapper contract

Before a separately approved switch, prepare and review each worker's own
run and check wrappers. Each hard-codes HOME and PEERS; neither takes a home,
peer list or worker identity from the caller. Use these exact peer sets:

| Wrapper | HOME under ~/dev/ribbitto/ | PEERS under ~/dev/ribbitto/ |
|---|---|---|
| Worker 1 | agy-worker-home | agy-worker-home-2, agy-trial-home |
| Worker 2 | agy-worker-home-2 | agy-worker-home, agy-trial-home |

Peer directories must exist before wrappers name them. Their existence does
not authorize sign-in or execution. Review runs retain the existing
[review boundary](running-argon.md#the-boundary).

Both workers' wrappers read verify-mode from worker 1's implementer/verify-mode.
Do not copy that switch into a separately editable worker 2 file. Continue
the existing idle-check rule: idle checks select off, while active checks
and runs use the trusted mode. Continue pinning the launcher hash and trusted
source as in the [switch record](argon-implement-switch.md#wrappers-and-trusted-inputs).
An updated binary needs newly generated home-specific trusted hooks; do not
make a mismatched configuration pass by bypassing byte comparison.

Before launch, verify SHA-256 equality with worker 1 for the common-content
files in each home's implementer directory: settings.json, the trusted agent
file argon-implementer.md and the trusted builtin.agy-<v>.txt list for the
selected agy version. These checks are wrapper policy; the Go runner checks
each home's trusted copies and baseline independently, without reading
worker 1's copies. Home-specific hooks, guard worktree files and sign-in
state are not common-content files. Signing in worker 2 and trusting its
builtin list are separate maintainer decisions, even if bytes compare equal.

Require the wrapper's worktree to be a direct child of ~/dev/ribbitto/wt/,
then pass its absolute path explicitly. Allocate a fresh per-run directory,
record its worker identity, and use an empty child log directory for Go.
Keep prompts and saved Git files outside all peer homes and writable trees.
Do not infer a peer set from whichever directories happen to exist or allow
an environment override to drop the review home. This PR does not install
or modify host wrappers.

## Operation and rollback

The global container sweep is uid-global, with liveness protection; it is
not partitioned by worker name. See [sweep](argon-verify-sweep.md) for its
refusals and dry-run behavior. After an abnormal end, run
`container-sweep --dry-run` before the next start of either worker, inspect
the result and perform the ordered manual recovery. Do one recovery at a
time. A dry run does not replace independently confirming session stop or
the at-rest check, and a released worktree lock is not evidence of stop.

The shared agy executable can auto-update while a worker is running. The
existing version/builtin checks fail closed when the new version has no
matching trusted list or the observed builtin bytes differ. Such a failure
after a run starts retains its recovery marker. The binary is shared, so coordinate updates
and approvals for both workers' trusted lists. The launcher does not freeze the executable during a run.

To disable worker 2, remove the execute bit from its run.zsh wrapper. This
does not stop an active session. Stop and recover active or abnormal runs
before further changes; do not remove a lock or marker to permit a start.

If the new launcher binary must also be removed, restore worker 1's complete
*.c00020d backup set of hooks.json, run.zsh and checks.zsh, including the
installed off hooks in the home configuration. Preserve and verify that
set before an approved switch; this document does not assert that the set
has already been created. Restore the pinned binary/source selection with
those wrappers. Then run the old checks.zsh idle check and one real run,
under the maintainer's approval, before considering rollback complete.
The [earlier rollback record](argon-implement-switch.md#recovery-and-rollback)
describes the preceding switch's different backup set; do not mix sets.
With no peers supplied, the Go peer checks preserve today's behavior;
removing peers is not a substitute for restoring a consistent trusted set.
