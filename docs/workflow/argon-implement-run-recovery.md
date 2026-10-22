# Implementer feature states and recovery

The [run command](argon-implement-run.md) and `implement-check` require
`--verify off|on`, explicitly, with no default. Unknown or absent states
are refused. `implement-check` still requires `--home`, `--worktree`,
`--git-file` and `--shared-settings-sha256`; `--agy` remains optional.
Both commands require `--trusted-source <abs>` with `on`; `off` forbids it.
The caller chooses `--verify`. At the switch, the host wrapper must derive
it from the maintainer's trusted verify-mode file; that wrapper is outside
this PR. Since 2026-10-22 the host wrappers do so
([switch record](argon-implement-switch.md)).
The worktree is always explicit.

## Feature states

Off compares home settings and hooks with the trusted implementer copies
and requires an existing regular, empty MCP file. The runner also verifies
that the trusted settings and hook copies equal the shared generators'
off bytes for the running binary and this home's guard directory.

On regenerates settings, the hook with `--allow-verify`, and the MCP
configuration for this worktree using the shared generators. All three
observed home files must match byte for byte; a mismatch is a problem.
The MCP file is nonempty only in this state. The source must be a real,
absolute, clean directory disjoint from home and worktree, at the commit
the running binary was built from, with no tracked, untracked or ignored
changes. The shared trusted-build check against source origin/main
still applies. Checks repeat before and after agy.

The exact-call guard and settings contract stays unchanged. Verify remains
refused outside that call. At rest the home always has off settings,
off hooks, an empty MCP file and no installed agent directory.

## Run sequence

1. Check the trusted binary, arguments, trusted off copies and safe paths.
   Generate the selected state read-only with the shared generators.
   Refuse pre-existing nonempty MCP configuration before any writes.
   Run the full off check on the untouched home and worktree before
   writing to the home, including before lock-file creation.
2. Acquire the exclusive nonblocking lock and recheck the marker.
   Create external exclusive logs, then exclusively create and durably
   sync the in-progress marker before any on-state write or agy start.
   A marker-creation or sync failure prevents installation and launch.
3. Install this worktree's generated configuration, agent and worktree
   marker, empty brain and pre-check the selected state.
4. Run agy under the existing supervisor. After confirmed stop, record the
   observed SHA-256 of settings, hooks and MCP in `post.txt` and sync it,
   then post-check the files agy ran with before restoring anything, in
   both modes. Record read errors and disqualify the run without skipping
   restoration. This preserves evidence that restoration would hide;
   off's post-check still catches a tampered or nonempty MCP file.
5. Restore off settings and hooks and empty the MCP file in both modes.
   Empty brain and remove TMPDIR.
   Remove the installed agent and directory. Attempt all applicable
   cleanup actions even if an earlier action fails. Run the final off
   at-rest check and sync its log.
6. Judge the transcript and save/sync status, including the selected feature
   state and generated settings, hook and MCP SHA-256 hashes. Deliver status.
   Only a clean result removes the marker and syncs its directory.
   A removal failure is reported, rewrites status with `counts=false` and
   leaves a refusal marker. Status initially says `in-progress-marker=remains`
   with its path. Only after durable removal, append and sync
   `in-progress-marker=removed`; the last such line decides. Require both
   `counts=true` and the final `removed` line to count: a crash between
   the initial save and removal leaves only `remains`. A failed final append
   rolls back the line, recreates the marker and saves `counts=false`.
   A surviving marker always overrides status.

The post-check happens after the supervisor's stop confirmation, because
reading or restoring files while a writer might survive would weaken the
existing unconfirmed-stop refusal. If stop is unconfirmed, retain the files,
brain, TMPDIR and agent for manual recovery; do not continue normal cleanup
or judge a transcript a live writer may still be changing. Every other
failure after placement attempts the off cleanup and at-rest check.
A failed restoration/check reports remaining paths and does not count.

## In-progress marker

The marker remains named `<home>/guard/implement-run.recovery-needed`.
It replaces the old marker created only after an unconfirmed stop.
Creation uses mode 0600, O_EXCL, no symlink traversal, file fsync and
parent-directory fsync. An existing entry of any type refuses the run.
The lock and marker live in the existing trusted guard-directory exemption.

The program removes the marker only after confirmed stop, post-check,
restoration, at-rest check, successful status save and otherwise clean
completion. Any failure retains it; a partial marker is also a refusal.
After every failed run, remove the marker by hand after the recovery check,
even if automatic cleanup passed.
If creation fails before leaving an entry, nothing was installed or started.
The lock is held through status and marker removal, and remains as a file
between runs.

SIGKILL or a crash releases the lock without stopping agy. Its process group
may survive. The marker survives installation crashes too, including a
partial installation with an on hook and an empty MCP file.

## Manual recovery

Before the first run after any abnormal end, do these in order:

1. Independently confirm that no process of the previous run's session
   remains. A released lock, an empty process-group query or retained
   transcript alone is not proof. Stop and confirm any survivors using
   the host's session procedure before changing files.
2. Before restoring, record whether the session's files were tampered with.
   For an on run, save the output of `implement-check --verify on
   --trusted-source <abs>` with the run's other check arguments, or compare
   all three files against the generated bytes or saved status hashes.
   For off, record the full off check. Preserve observed hashes/read errors
   too; `post.txt` has these only if normal completion reached observation.
3. Restore the feature-off files: settings and hooks from the verified
   trusted off copies and an empty regular MCP file. An abnormal end
   during an on run leaves on-state files in the home. Inspect the reported
   paths, empty brain, remove the previous TMPDIR, and remove the installed
   agent and its directory. The run leaves `guard/implementer-worktree` at
   rest containing the absolute run worktree path followed by a newline;
   check that value rather than restoring an unspecified previous path.
   Revalidate the trusted off copies against `implement-config --mode off`
   for this binary and guard directory before using them.
4. Run the full `implement-check --verify off` with the home, worktree,
   saved Git file, maintainer's shared-settings hash and intended agy path.
   Require success, including home, settings, hooks, MCP, builtin files,
   agent directory and protected worktree checks.
5. Only then remove the stale marker by hand. Do not run again if restoration
   or the at-rest check fails. Keep the failed run's output uncounted.

The host comparison, real agy preflight in both modes, the failure cases and
the rollback drill were recorded on
[#1436](https://github.com/tkakkie/ribbitto/issues/1436) when Stage 2 switched.
