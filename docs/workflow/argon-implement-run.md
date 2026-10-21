# Implementer run command

`argonreview implement-run` adds the implementer sequence to the
trusted Go launcher. It is **not in use until
[#1436](https://github.com/tkakkie/ribbitto/issues/1436)** compares the two
runners and verifies the switch. The existing `run.zsh`,
[Start gate and boundary](argon-implementer.md#the-boundary), and
[verify status](argon-verify.md#status) still apply.

## Arguments

```sh
argonreview implement-run --verify off|on [--trusted-source <abs>] \
  --home <implementer-home> --worktree <dir> \
  --git-file <saved-file> --prompt-file <file> --log-dir <dir> \
  --shared-settings-sha256 <64-hex> [--agy <path>] [--model <name>] \
  [--timeout <duration>]
```

Each flag is accepted once; positional arguments are refused. Paths must be
absolute, clean and free of symlink components. The home and worktree must
be real directories that do not overlap. The implementer home must also
not equal or contain the calling account's home, or equal, contain or lie
inside its shared `.gemini`. A dedicated home elsewhere under the account
home is accepted. Comparisons use literal paths and the review launcher's
directory identities up the resolved chains, protecting shared `.gemini`
even through symlinks or case-insensitive aliases. Logs and the saved `.git` file
must be outside the worktree and home; the log directory cannot contain
either. The saved file must be regular with one hard link. The prompt is
read from outside the worktree, capped at 1 MiB; the saved file is capped
at 64 KiB. Calling `HOME` must be absolute and clean. The worktree must be
valid UTF-8 and unchanged by the existing guard's whitespace/separator trim;
hook paths retain the generator's apostrophe/newline refusal.

The hash is required, accepts uppercase hex, and comes from the maintainer;
it is never derived from the shared file. Defaults are
`$HOME/.local/bin/agy`, the shared model constant (`gemini-4-argon-high`),
and `20m`. A model change requires the maintainer's choice. The print limit
is the supervisor limit minus two minutes, or half the limit when that
would be nonpositive, as for reviews.

Both explicit and default agy paths must name a regular executable file
(at least one executable mode bit), with no symlink leaf or component.
They must be outside the worktree, implementer home, log directory and
the newly created TMPDIR. The first three are checked during validation;
TMPDIR is checked before any agy invocation, including the pre-check.
An existing MCP configuration must be an empty regular file; non-empty
configuration is refused before writes, preserving feature-on settings.
An absent MCP file fails the full initial at-rest check.

## Sequence

1. Check the trusted build, before parsing arguments. Validate all inputs
   and configuration targets. Generate feature-off settings and hooks
   using the resolved running binary and `<home>/guard`; require exact
   byte equality with the home's trusted implementer copies before writing.
2. Run the full feature-off at-rest check on the untouched home and worktree;
   a failure refuses before writing anything to the home. Acquire an exclusive
   non-blocking flock on
   `<home>/guard/implement-run.lock`, opened mode 0600 through the confined
   directory descriptor without following links. A concurrent run refuses
   before changing the home, logs or TMPDIR. Hold the lock through cleanup
   and status/output. Its descriptor is close-on-exec; the supervisor passes
   only stdin/stdout/stderr to agy. Refuse an existing
   `<home>/guard/implement-run.recovery-needed` marker of any type before
   creating logs or TMPDIR. Both persistent files belong under the home
   scan's existing `guard/` exemption. If the lock file is missing, a
   read-only marker check also prevents recreating it on a recovery refusal;
   the marker is always rechecked with the lock held.
3. Create a mode-0700 log directory, or accept an existing real empty one.
   Exclusively create mode-0600 status, transcript, stderr, pre-check and
   post-check files without following links. Keep their descriptors open.
   Durably create the exclusive in-progress marker before placement;
   see [states and recovery](argon-implement-run-recovery.md).
   Create a fresh random exclusive TMPDIR beside the worktree.
4. Place the selected state's generated settings, hooks and MCP file, the guard's
   worktree path plus newline, and a mode-0400 byte copy of the trusted agent.
   Home writes refuse symlinks and non-regular leaves, replace regular
   leaves (including a read-only agent left from an earlier run) with exclusive
   creation and fsync the bytes. Empty only the
   children of the real `brain/` directory, without following links.
5. Call `implement-check` in-process with the home, worktree, saved file,
   maintainer's hash and agy path. A failed pre-check prevents launch.
6. Before launch, consume any signal buffered during placement or pre-check
   without blocking. If one is pending, record `interrupted by <signal>`,
   set exit to 128 plus the signal number and perform cleanup without
   launching agy. Otherwise run
   `agy -p <prompt> --model <model> --agent argon-implementer --sandbox
   --print-timeout <print-limit> --output-format stream-json` from the
   worktree, stdin closed, using only `agyEnvironment`'s environment.
   Its parent environment has PATH replaced with the fixed trusted value
   `/usr/bin:/bin:/usr/sbin:/sbin:<accountHome>/.local/bin`, using the validated
   calling account home, never the caller's PATH. Other allowlisted variables
   retain the shared helper's behavior.
   The existing supervisor uses the limit, five seconds' grace and INT/TERM
   handling, then stops and confirms the session.
7. Follow the [completion and recovery sequence](argon-implement-run-recovery.md):
   post-check before feature-off restoration in both modes, at-rest check,
   saved status, then marker removal after confirmed stop. A failed
   step disqualifies the run and keeps the marker. An unconfirmed stop
   retains the files for manual recovery, as before.
8. Restore off settings, hooks and empty MCP after the post-check; empty
   brain and remove TMPDIR, agent and directory, then run the at-rest check.
   Attempt every applicable cleanup step even if another fails.
9. Apply the shared transcript judge after cleanup and write status.
   Signals during completion are reported after cleanup.

There is no review worktree `record`/`verify`: implementation edits are
expected; `implement-check` checks the protected baseline before and after.
Both commands require an explicit feature state; see
[states and recovery](argon-implement-run-recovery.md).

## Results and recovery

Logs are `out.jsonl`, `err.txt`, `pre.txt`, `post.txt`, `at-rest.txt` and `status` in the
trusted log directory. Transcript judgement reads held descriptors, capped
at 16 MiB per agy log, so a later pathname replacement cannot substitute
evidence. Excessive logs fail judgement.

Status uses `key=value` lines: `exit` is agy's exit status (`-1` if never
launched, except an interrupted run records 128 plus the signal number),
`counts` is a boolean, `pre-check` and `post-check` are check
exit statuses (`-1` if not run), `stop-confirmed` and `session-left` describe
session cleanup. Each process stopped after agy exits adds one quoted
`straggler=<command line>` line, captured before stopping it; a clean run
has none. Each command keeps at most 4096 bytes at a UTF-8 boundary, with
`...(truncated)` appended inside the quotes when truncated.
These lines do not change whether the run counts; an unconfirmed
stop still disqualifies it. Status also records `verify` and the three generated-file
SHA-256 hashes. Repeated quoted `reason` lines explain failure;
`cleanup-*` booleans describe attempted steps. `recovery` gives the next
action; `recovery-inspect` names paths to inspect after other failures;
`manual-recovery-needed` lists retained paths on unconfirmed stop, and
`recovery-marker` names the persistent marker. Status is also printed to
stdout. A failed status save rebuilds the status with `counts=false` and
the log/status failure reason, retries saving it and prints the rebuilt
status. Failed printing likewise disqualifies the run and attempts to save
rebuilt status with the output failure; errors are joined and reported.
Early validation failures only diagnose
on stderr and write nothing.

A run does not count on nonzero agy exit, timeout, INT/TERM, `AGY_ERROR`,
malformed or incomplete transcript, denied actions, another shared judge
refusal, changed shared settings, failed pre/post-check, unconfirmed stop,
or any placement, cleanup or log failure. Exit is zero only for a counted
run; preserve 124 for timeout, 130/143 for INT/TERM, and other nonzero agy
statuses. Invalid arguments exit 2; other failures exit 1 unless an agy or
signal status applies.

For any abnormal end, follow the ordered
[manual recovery procedure](argon-implement-run-recovery.md#manual-recovery)
before the first subsequent run. SIGKILL or a crash releases the lock without
stopping agy; its process group may survive. The marker remains until recovery.
The program removes it only after a clean end; failures require manual removal
after recovery. Do not treat retained output as counted.

## Differences from run.zsh

Deliberate safer changes: no force, guard override or skip-pre-check path;
trust first; required external expected hash; validation before writes;
trusted inputs and exclusive logs outside writable trees with symlink and
containment checks; single-link saved comparison; random TMPDIR; supervisor
session confirmation replacing the pgid/pgrep loop; unconfirmed stop blocks
completion; cleanup after failures and cleanup failures disqualify the run;
generated configuration must equal trusted copies; settings as well as
hooks are restored; shared judge and bounded judgement replace exit-only
recording. Home writes use exclusive creation and fsync.

The fixed trusted PATH matches the script while retaining the unchanged
shared `agyEnvironment` helper. Locale variables are
forwarded through that helper rather than hardcoding `LANG=en_US.UTF-8`.

Until #1436 compares runners, generated Go hooks must equal production
`<home>/implementer/hooks.json` bytes (already using the Go guard after
Stage 1). Adding this command switches no workflow.
