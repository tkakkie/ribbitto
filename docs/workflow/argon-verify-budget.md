# Verification memory budget

The budget, per-check wiring and host start procedure are merged code, **NOT in force** until a
separately approved launcher update (see [enabling preconditions](#host-lock-provisioning)).
This page describes the candidate mechanism, not approval to provision the host,
start containers or update a launcher. The combined security review, preflight
and enabling conditions of [#1908](https://github.com/tkakkie/ribbitto/issues/1908)
still apply to every caller, including both workers and host commands.

## Reservations and cleanup

The Docker VM budget is three units of **2 GiB**. Every current check takes
one unit (`budgetSingle`) before even creating its source-copy directory:
`build`, `vet`, `test`, `generated`, `test-db` and `container-generate`'s
internal writeback check. `container-check`, `verify-mcp` and
`container-generate` share `runContainerExecution`; a missing budget refuses
execution. The internal browser runner takes two units; exposure awaits B3b.
A two-unit reservation means 4 GiB.

Units use nonblocking `flock` on slot files. A one-unit request takes the lowest
usable slot. The global lock order is `browser.lock`, then slot 0, 1, 2.
Every acquirer takes its complete lock set or none, releasing partial sets
before sleeping; nothing is held while waiting. The lock primitive supports
browser and exclusive callers; host starts use [budget-start](argon-verify-budget-start.md#start-procedure).

The container receives `argonreview.slot=<k>` (comma-separated for multiple
units); both memory and swap equal units times 2 GiB (`2g` for one unit).
Other container arguments and the fixed run/name/network client prefix stay
unchanged. The runner finishes container cleanup and removal, removes the
source copy and closes its copy lock, then releases its units. Failed removal
is reported before release: the leftover's label continues holding its slot,
even after process death releases its file lock. A leftover keeps its slot
until the [sweep](argon-verify-sweep.md) reclaims it; slot labels give the sweep
no new authority. A source-cleanup failure still fails the check.

## Reconciliation and refusal precedence

One complete Docker snapshot feeds slot reconciliation and M2 classification.
The adapter reuses M2's inspection-presence checks: missing or null memory and
swap fields are unavailable, not zero. An explicit zero remains evidence of
an unlimited container. Failed inspections need a fresh listing to confirm
disappearance; malformed inspection evidence is carried across snapshot retries
and stays unavailable even if the ID later disappears. A new ID in that listing
retries the entire snapshot, at most three whole-snapshot attempts in total;
persistent arrivals are unavailable. All listing, inspection,
confirmation and retry calls share a **30-second total deadline**, inside the
caller's context, in addition to each command's 20-second limit. Expiry means
the container state could not be determined.

The hook calls M2's classifier and reservation-consistency function. It checks
the name, owner, pinned image, one or two slots, browser class exactly for two
slots, memory exactly slots times 2 GiB, and equal swap. M1b additionally
requires strictly ascending slot labels. Verification containers in any state
remain held by M1b; the hook contributes only registered dev containers in
created, running, paused or restarting states. A consistent verification
container is counted once; a dev container sharing its slot is a duplicate.
Nothing in this observation path starts, stops or removes a container.

The first matching row below wins, independently of inventory order. Every
line begins with `NOT VERIFIED: verification budget ` followed by the exact
suffix in the table. M2's full lines are reused without copying their text.

| Rank | Exact suffix |
|---|---|
| 1 | unavailable: the container state could not be determined |
| 2 | unavailable: an orphaned Docker client is present |
| 3 | unavailable: a verification container does not match its reservation |
| 4 | unavailable: a verification container has no valid slot label |
| 5 | unavailable: a registered container does not match its registration |
| 6 | unavailable: a container has no memory limit |
| 7 | unavailable: an unregistered container is present |
| 8 | unavailable: a slot is claimed twice |
| 9 | paused: an exclusive container is present |

First establish whether the observation itself can be trusted, then identify
which container is wrong, then check slot arithmetic. Pause only when nothing
is unavailable. M2's ordering is a subsequence of this order, so its highest
cause preserves the evidence needed to combine both layers. With no such
cause and no free reservation, the suffix is `busy`. One **120-second deadline**
bounds the whole reservation: process discovery, inventory reads, lock attempts and sleeps, with
one-second polling inside the ten-minute run context, before any source copy.
It returns the last cause from a completed attempt, or
`unavailable: the container state could not be determined` if none completed.
Its own expiry is never lock-wait interruption;
parent cancellation keeps the existing cancellation semantics. The 120 seconds
does not bound the container run or cleanup, or replace the ten-minute timeout.
The 20-second per-Docker-call limit and 30-second total inventory deadline still
apply inside the reservation deadline. There is no automatic retry after refusal.
Refusals start no container
and reach both the host report and the bounded MCP summary, followed by scope.
They exit non-zero with no runner error unless a separate failure occurred.


Lock safety and invalid requests have these exact fixed lines:

```text
NOT VERIFIED: verification budget unavailable: lock files are unavailable or unsafe
NOT VERIFIED: verification budget unavailable: invalid lock set
NOT VERIFIED: verification budget unavailable: lock wait interrupted
```

The last line is the budget layer's interrupted-lock-wait result. A canceled
run context instead keeps the existing interruption error and verdict rules;
it is never relabelled budget busy. An expired run uses
`NOT VERIFIED: time limit exceeded`; MCP cancellation before a container uses
`NOT VERIFIED: cancelled`. The ten-minute run limit, outer MCP call limit and
summary limits are unchanged (2,048-byte small results; 7,000-byte MCP cap).

## Host lock provisioning

All enabling preconditions are collected here:

1. `/var/cache/argonreview/budget/` must exist as a real directory owned by the
   uid that runs every caller (for example mode 0700). The same applies to
   `/var/cache/argonreview/`, the parent of `browser.lock`; neither may be
   group- or world-writable. The runner never creates these directories.
   Until they exist, every check gives
   `NOT VERIFIED: verification budget unavailable: lock files are unavailable or unsafe`.
   Slot files and `browser.lock` then self-create at 0600. All callers, including
   both workers' wrappers and Claude's `container-check`/`container-generate`,
   must run as that uid; every sandbox must allow file creation and flock there.
2. Registered dev containers started through `budget-start` are counted;
   exclusive containers pause new checks. Every other container in a counted
   state (created, running, paused or restarting) blocks all checks, including
   a plain `docker run` Codex database or `make db-up` run by habit. Follow the
   [database and start procedures](argon-verify-budget-start.md).
3. Leftovers from the old binary have no slot label and block every check.
   Run the old launcher's `container-sweep` before switching, following
   [#1700's sweep rule](argon-verify-sweep.md).
4. The budget holds only when every container-starting caller uses the new
   build. An older binary takes no slot and sets no slot label.
5. Merging does not change the enabled launcher: production stays on
   `argonreview-2e3a6bf` until a separately approved launcher update.

Each immediate parent is opened with `O_DIRECTORY|O_NOFOLLOW|O_CLOEXEC`,
checked through its descriptor, and retained while its file is opened.
A symlink in place of either immediate parent is refused.

The files are `/var/cache/argonreview/budget/slot-0.lock`,
`/var/cache/argonreview/budget/slot-1.lock`,
`/var/cache/argonreview/budget/slot-2.lock` and
`/var/cache/argonreview/browser.lock`. Each opened file must be regular,
owned by the calling uid, exactly mode 0600 (including no special mode bits),
and have one link. File symlinks are refused. Opening uses
`O_RDWR|O_CREAT|O_NOFOLLOW|O_CLOEXEC|O_NONBLOCK`, mode 0600; exclusive creation
handles simultaneous first callers. Missing files can be created in safe
parents, but missing/unsafe parents or files that cannot be safely opened,
created or locked fail closed with the lock-files refusal above. These checks
apply to every requested lock; current one-unit checks request only slot locks.

## Fail-closed coverage and limits

The wiring enforces N1/N2 through the production slot reconciler and classifier:
listing/inspection errors and unknown states; missing, malformed, out-of-range
or non-ascending verification slot labels; duplicate slots; mismatched names,
owners, pinned images, unit counts, browser classes, memory or swap; orphaned
Docker clients of this uid with the runner prefix; unsafe lock files; invalid
dev registrations; unlimited dev containers and unregistered counted
containers. An exclusive counted container pauses new reservations. All
verification states hold slots; foreign containers count only when created,
running, paused or restarting. Discovery is read-only and never stops, removes
or changes a foreign container (N3).

These detect conflicts and block **new** runs; they cannot guarantee against a
container started during a run outside the coordinated start procedure, a
labelled container started with plain `docker start`, `docker update` between
polls, another Docker engine, or VM-level memory use. An out-of-memory kill is
never verified. No browser behavior is enabled by this wiring.

## Database and start procedures

[Host starts and database lifecycles](argon-verify-budget-start.md) are merged,
**NOT in force** until the separately approved launcher update; no host action is authorized here.
