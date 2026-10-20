# Implementer run command

`argonreview implement-run` adds the feature-off implementer sequence to the
trusted Go launcher. It is **not in use until
[#1436](https://github.com/tkakkie/ribbitto/issues/1436)** compares the two
runners and verifies the switch. The existing `run.zsh`,
[Start gate and boundary](argon-implementer.md#the-boundary), and
[pending verify status](argon-verify.md#status) still apply.

## Arguments

```sh
argonreview implement-run --home <implementer-home> --worktree <dir> \
  --git-file <saved-file> --prompt-file <file> --log-dir <dir> \
  --shared-settings-sha256 <64-hex> [--agy <path>] [--model <name>] \
  [--timeout <duration>]
```

Each flag is accepted once; positional arguments are refused. Paths must be
absolute, clean and free of symlink components. The home and worktree must
be real directories that do not overlap. Logs and the saved `.git` file
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

## Sequence

1. Check the trusted build, before parsing arguments. Validate all inputs
   and configuration targets. Generate feature-off settings and hooks
   using the resolved running binary and `<home>/guard`; require exact
   byte equality with the home's trusted implementer copies before writing.
2. Create a mode-0700 log directory, or accept an existing real empty one.
   Exclusively create mode-0600 status, transcript, stderr, pre-check and
   post-check files without following links. Keep their descriptors open.
   Create a fresh random exclusive TMPDIR beside the worktree.
3. Place the verified settings and hooks, an empty MCP file, the guard's
   worktree path plus newline, and a byte copy of the trusted agent.
   Home writes refuse symlinks and non-regular leaves, replace regular
   leaves with exclusive creation and fsync the bytes. Empty only the
   children of the real `brain/` directory, without following links.
4. Call `implement-check` in-process with the home, worktree, saved file,
   maintainer's hash and agy path. A failed pre-check prevents launch.
5. Run `agy -p <prompt> --model <model> --agent argon-implementer --sandbox
   --print-timeout <print-limit> --output-format stream-json` from the
   worktree, stdin closed, using only `agyEnvironment`'s environment.
   The existing supervisor uses the limit, five seconds' grace and INT/TERM
   handling, then stops and confirms the session.
6. If stopping cannot be confirmed, stop normal completion immediately.
   Do not restore configuration, empty brain, remove TMPDIR, run the
   post-check or remove the agent. Report manual recovery and retained paths.
   Conservatively, any supervisor error after process start takes this path.
7. Otherwise restore verified settings and hooks, empty brain and remove
   TMPDIR. Attempt every cleanup step even if another fails. Run the
   post-check with the agent still present, then remove the agent and its
   directory after the check has run, regardless of its result.
8. Apply the shared transcript judge after cleanup and write status.
   Placement failure, pre-check failure and startup failure also perform
   applicable cleanup and the post-check. Signals during completion are
   reported after cleanup.

There is no review worktree `record`/`verify`: implementation edits are
expected; `implement-check` checks the protected baseline before and after.
Verify stays off: no MCP permission or configuration is enabled.

## Results and recovery

Logs are `out.jsonl`, `err.txt`, `pre.txt`, `post.txt` and `status` in the
trusted log directory. Transcript judgement reads held descriptors, capped
at 16 MiB per agy log, so a later pathname replacement cannot substitute
evidence. Excessive logs fail judgement.

Status uses `key=value` lines: `exit` is agy's exit status (`-1` if never
launched), `counts` is a boolean, `pre-check` and `post-check` are check
exit statuses (`-1` if not run), `stop-confirmed` and `session-left` describe
session cleanup. Repeated quoted `reason` lines explain failure;
`cleanup-*` booleans describe attempted steps. `recovery` gives the next
action; `recovery-inspect` names paths to inspect after other failures;
`manual-recovery-needed` lists retained paths on unconfirmed stop.
Status is also printed to stdout. Early validation failures only diagnose
on stderr and write nothing.

A run does not count on nonzero agy exit, timeout, INT/TERM, `AGY_ERROR`,
malformed or incomplete transcript, denied actions, another shared judge
refusal, changed shared settings, failed pre/post-check, unconfirmed stop,
or any placement, cleanup or log failure. Exit is zero only for a counted
run; preserve 124 for timeout, 130/143 for INT/TERM, and other nonzero agy
statuses. Invalid arguments exit 2; other failures exit 1 unless an agy or
signal status applies.

For manual recovery, first independently confirm the session is gone;
then inspect the reported paths, restore the trusted settings and hooks,
empty brain, remove TMPDIR, run the post-check with the trusted agent
present, and remove the agent and directory. Do not start another run
until restoration and checks pass. Do not treat retained output as counted.

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

Open question for the maintainer: `agyEnvironment` forwards parent `PATH`,
whereas the script fixes it; a fixed trusted PATH would be safer. This
command deliberately reuses the required helper. Locale variables are
forwarded through that helper rather than hardcoding `LANG=en_US.UTF-8`.

Until #1436 compares runners, generated Go hooks must equal production
`<home>/implementer/hooks.json` bytes (already using the Go guard after
Stage 1). Adding this command switches no workflow.
