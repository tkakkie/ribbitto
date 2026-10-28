# Argon browser check

**Merged but not in force:** the browser runner and admission checks
are merged; nothing here enables or authorizes anything. The
[preflight and separate approvals](argon-verify-browser-preflight.md) govern
adoption. [Verify operation](argon-verify-operation.md) retains the current
permission. CI stays the check.

## Named check and boundary

`test-browser` runs one named go-rod test on regenerated sources with
PostgreSQL 18. The fixed script uses
`go test -count=1 -json -p 2 -run "$2" "$1"`; the package and anchored run
pattern are positional arguments, never interpolated into the script.
`RIBBITTO_REQUIRE_BROWSER=1` makes an unavailable browser fail instead of
skip. The database environment is the same fixed environment as `test-db`.
No caller selects an image, command or resource limit.

The [source-copy boundary](argon-verify.md#verification-container) stays:
no original worktree or dedicated home mount; network `none`, read-only
root, uid/gid `1000:1000`, `--cap-drop=ALL`, `no-new-privileges` and Docker's
default seccomp. Never add a seccomp profile, `--privileged`, `--cap-add`,
`--userns`, `--ipc=host`, `--device`, `--add-host` or a published port.
The browser reservation is two 2 GiB units: `--memory 4g --memory-swap 4g`,
`--pids-limit 2048` and `argonreview.class=browser`, plus the slot label.
Two CPUs, the 1 GiB tmpfs mounts and ten-minute run limit stay unchanged.
The [aggregation limits](argon-verify-aggregation.md#limits-and-retention)
apply, including the 32 MiB total stdout/stderr stream cap. Never set `CI`,
browser shard/count variables or measurement gates.

Every non-browser check, including write-back, has
`--tmpfs /usr/lib/chromium:ro,noexec,nosuid,nodev,size=1m`; only
`test-browser` omits it. Follow the real-image
[mask preflight](argon-verify-browser-preflight.md#chromium-mask-preflight)
and [update order](argon-verify-browser-updates.md#update-path).

## Call arguments

The `verify` schema includes `test-browser`. Its
[acceptance rules](argon-verify-browser-arguments.md#call-arguments) cover
required keys, package and run validator; they do not authorize a call
before enabling.

## Switch and admission order

The trusted `browser-mode` file sits beside `verify-mode` in worker 1's
`implementer/`, outside the repository: file 0400, directory 0500, like
`verify-mode`. Write it as for
[the verify switch](argon-implement-switch.md#wrappers-and-trusted-inputs),
restoring those permissions. Switching to `on` requires the maintainer's
separate approval; provisioning never enables a check.

Both workers' run and check wrappers pass the **same absolute path** as
`--browser-mode <abs path>` only when `--verify on`; worker 2 uses worker 1's
file, never a separate copy. With `--verify off` (`checks.zsh` idle check or
`verify-mode off` rollback), they never pass it: those commands refuse the
flag, so an unconditional flag would make them refuse.
`container-check` passes the same path for `test-browser`. The flag is on
`implement-run`, `implement-check`, `implement-config`, `verify-mcp` and
`container-check`. The implementer commands require verify on (`--mode on`
for config). The path goes into `mcp_config.json`; `implement-check`
regenerates and compares it byte for byte, including that path.
Paths must be absolute, clean, valid UTF-8, non-root, with no single quote
or control character.

The guard checks argument shape only through `parseVerifyArguments` and
the shared package/run validators. The runner reads the switch and, if on,
the gate on every call, never at server start or in the guard.
Every `test-browser` call reads the switch before any units or source copy.
The opened file must be regular, uid-owned, not a symlink, and not group-
or world-writable. Only bytes `on` or `on` plus one newline enable admission.
An absent flag, unreadable/missing/unsafe file or any other content gives
`NOT VERIFIED: browser check paused`. This takes precedence over the gate.

Next the runner reads the [security gate](argon-verify-browser-record.md).
Then it reserves `browser.lock` at `/var/cache/argonreview/browser.lock`
and two slots, all or none, in order browser, slot 0, 1, 2. It polls
nonblocking for at most 120 s inside the ten-minute run context, holding
nothing while waiting. Contention gives `NOT VERIFIED: browser check busy`;
there is no automatic retry. All other causes keep the
`NOT VERIFIED: verification budget ` prefix and the exact
[budget suffixes and precedence](argon-verify-budget.md#reconciliation-and-refusal-precedence).
Those include exclusive pause, unknown state, unsafe locks, invalid
reservations, unlimited/unregistered containers, duplicate slots and orphans.
Refusals start no container and create no source copy.

After reservation, the runner rechecks switch/gate before copying; refusal
releases the reservation.
After admission: reserve, copy, run, stop/remove, remove source copy and
close its lock, release units. Failed removal never makes a leftover's
labelled slots free; use the [sweep](argon-verify-sweep.md).

## Judgement and exact result lines

`verified` requires the targeted package, the named node's own single `run`
and `pass`, every executed descendant passing, only the node, descendants
and segment ancestors, and 1–64 passing leaves. A group with only child
events cannot stand in for the named node. No skip allowlist applies.
Existing failure and skip rules take precedence over named-scope checks.
Success includes `named test: <name> passed (<k> leaf tests)`; the summary
includes the complete package and run in its `Check:` line.

The browser-specific refusal lines are:

```text
NOT VERIFIED: browser check paused
NOT VERIFIED: browser check paused: security check overdue
NOT VERIFIED: browser check busy
NOT VERIFIED: the named test did not run: <name>
NOT VERIFIED: a test outside the named test ran
NOT VERIFIED: too many tests ran (<k>, limit 64); name fewer tests
```

Inherited test-stream refusals remain exact; see
[N1 verdict precedence](argon-verify-aggregation.md#n1-verdict-precedence):

```text
FAILED
FAILED: empty or truncated test stream
FAILED: malformed test stream
FAILED: event without package
FAILED: duplicate or invalid package start
FAILED: event outside a running package
FAILED: test without name
FAILED: duplicate test
FAILED: event outside a running test
FAILED: result without a running test
FAILED: unknown test action
FAILED: missing package result
FAILED: missing test result
NOT VERIFIED: <k> skipped
NOT VERIFIED: a targeted package has no tests, or no leaf test passed
NOT VERIFIED: output limit exceeded (output cut off)
FAILED: output limit exceeded after a confirmed failure (output cut off)
```

Skipped names accompany the skipped count. Nonzero exit, runner error,
build failure, panic, incomplete/malformed streams and an out-of-memory kill
never verify. Timeout uses `NOT VERIFIED: time limit exceeded`.
Source removal failure uses `FAILED: source cleanup failed`.
Result handling also retains `NOT VERIFIED: cancelled`,
`FAILED: the check returned an error`, `NOT VERIFIED: the check could not run`
and `NOT VERIFIED: summary inconsistent with verdict` for its existing
[result handling](argon-verify-testdb.md#timeout-and-cancel-results).
Record-command diagnostics are [separate](argon-verify-browser-record.md#refusals).

Scope names Debian Chromium's recipe version, sandbox disabled with the
container as the only boundary, not CI's Chrome or a browser-targets record,
only the named test, and no race detector. **Stdout is not authenticated:**
Argon's own test code can print test2json events in the same uid and
container. `verified` is Argon's own check, never a security control.
CI stays the check; review and handoff checks remain required.

## Removal authority and emergency pause

The five [removal cases](argon-verify-budget-start.md#stop-and-removal-authority)
stay distinct: runner cleanup of its own random name; sweep of a twice
validated dead leftover by full ID; Claude's browser emergency stop;
`budget-start` stopping only its just-started ID on failure; and Claude's
recorded-ID Codex database lifecycle. Labels alone give no removal authority.
Nothing here permits touching foreign, unregistered, exclusive or unknown
containers.

On exploitation in the wild, Critical severity, container escape, or an
exploitable High or above with serious impact without the sandbox, Claude
takes these steps at once, in order:

1. Write `off` to `browser-mode`, using the verify-switch permission change
   and restoring file 0400/directory 0500.
2. List browser containers by label `argonreview.class=browser`.
3. Apply emergency removal case 3 to each: inspect the full 64-hex ID and
   require name `argonreview-check-` plus 32 lowercase hex digits,
   `argonreview.owner=verify`, `argonreview.class=browser` and the pinned
   verify image. It may be live. Only after the switch is off, `stop` and
   `rm -f <ID>` that exact ID and confirm absence; record the action.
4. Run `container-sweep` under its unchanged ownership and liveness rules.
5. Wait until no source copy is [in use](argon-verify-sweep.md#liveness):
   `container-sweep --dry-run` must report no `skipped (in use)` copy.
   List `argonreview.class=browser` again and repeat steps 3 and 4 until no source
   copy and no browser container remains and every slot is free.
   If it does not clear, **do not proceed to enabling or re-enabling**.
6. Record the pause and cleanup and tell the maintainer.

The pause procedure requires the runner to check the switch and gate again
immediately before `docker run`; without that check, a call already copying
when `off` was written can still start Chromium (up to the ten-minute run
limit), which is why step 5 waits for copies and repeats steps 3 and 4.

Re-enabling always needs the maintainer. Other security fixes immediately
start the [update path](argon-verify-browser-updates.md#update-path).
