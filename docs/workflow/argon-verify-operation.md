# Argon verify operation

The [verify permission](argon-verify.md#status) is in force since
2026-10-21. Production uses the [Go runner](argon-implement-switch.md)
since 2026-10-22 (#1436 Stage 2).
After reading #1433's record, the maintainer approved enabling in chat
that day. Evidence: [step 1][step1], [preflight][preflight],
[enabling and passing smoke test][smoke]. Stage 2 is independent of enabling.
On 2026-10-27 the maintainer approved [#1700](https://github.com/tkakkie/ribbitto/issues/1700)
stage A: both workers run `argonreview-2e3a6bf` and its verify image
(generator checks, `test-db`, summary). Its window passed the smoke tests, a
rollback to `7f607ca` and a second switch; the record is on #1700. `agy` does
not offer `run_command`, so that refusal was not seen in the agent: the guard's
and the settings layer's own refusals cover it.

The [verify image recipe](argon-verify-image.md) describes the host build procedure;
each build needs the maintainer's approval in chat.

## Memory-budget reconciliation (not in force)

The budget primitives and combined inventory are merged code, **not in force**.
Production remains on `argonreview-2e3a6bf`; entry-point wiring, per-check
reservations and the budget page arrive in #1908 M3b. A later binary needs
a separately approved launcher update. This section describes M3a's inventory
contract, not an instruction to change the host or start a container.

One complete Docker snapshot feeds slot reconciliation and M2 classification.
The adapter reuses M2's inspection-presence checks: missing or null memory and
swap fields are unavailable, not zero. An explicit zero remains evidence of
an unlimited container. Failed inspections need a fresh listing to confirm
disappearance; malformed inspection data stays unavailable even if the ID
disappears. A new ID in that listing retries the entire snapshot, at most
three attempts; persistent arrivals are unavailable. All listing, inspection,
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
cause and no free reservation, the suffix is `busy`. Lock-file safety,
invalid requests and interruption keep their existing fixed refusal lines.
The reservation layer polls every second for at most 120 seconds and returns
the last observed cause; M3b must apply that wait inside the ten-minute run
context before copying source and preserve refusals in reports and summaries.

## Switch and run

The trusted `verify-mode` file in the maintainer's harness, outside any
repository, holds `on` or `off`. Any other value stops the run.
It is `on` as of 2026-10-22.

The complete config-generator invocation is:

```text
implement-config --mode off|on --binary <abs> --guard-dir <abs> [--trusted-source <abs> --worktree <abs>] --out <dir>
```

Both modes require `--mode`, `--binary`, `--guard-dir` and `--out`.
On mode requires `--trusted-source` and `--worktree`; off mode forbids them.
All paths, including `--out`, must be absolute and clean.
The output contains `settings.json`, `hooks.json` and `mcp_config.json`.
`--out` must be absent or an existing empty real directory.

In `on` mode:

1. The `run.zsh` wrapper calls `argonreview implement-run --verify on`.
   Using the shared config generators, it generates the run's settings,
   hook (with `--allow-verify`) and MCP file for that worktree, then
   installs them in the dedicated home.
   It refuses a guard override.
2. Before the run, `implement-run` calls `implement-check` in-process to
   regenerate and compare all three files byte for byte. The commands
   require the trusted source to match the running binary's revision and
   be clean, including untracked and ignored files. `checks.zsh` is a
   wrapper around `argonreview implement-check`.
3. After confirmed stop, `implement-run` records the observed file hashes
   and calls `implement-check` again on the files agy ran with, before
   anything is restored. It checks the same generated bytes and pinned
   clean source again; status records the three generated SHA-256 hashes.
4. The home returns to feature-off settings, hook and empty MCP file;
   an idle check confirms it. At rest the home never holds the permission.

On-mode MCP admits only the exact `verify` call through agreeing guard and
settings layers. In [preflight][preflight], the guard refused every invalid
call first; the settings and server layers were not probed separately.
Arbitrary shell and `run_command` remain refused. Browser tests stay CI-only. `verified` never replaces CI, review or handoff
checks. The Start gate and review independence stay.

## Write-back at handoff

Generated-file write-back is **not in force until #1700's second stage (B)**.
Once enabled, Claude runs the trusted host command
`argonreview container-generate <worktree>` only after the supervisor confirms
no run process remains and all post-run checks pass, before reading the diff.
Never run it during an Argon run or through MCP; `generated` during a run stays
report-only. Use the same trusted source/build gate as `container-check`.
The [write-back rules and report](argon-verify-writeback.md)
cover refusals, allowed paths, hashes and partial replacement.

Add the full report and exit status to the handoff record. A refusal or partial
replacement stops handoff for investigation; already replaced files stay
replaced. Retrying skips identical files. Review stale outputs explicitly:
nothing is deleted. This host step runs isolated generators only inside the
container and does not relax the rule against running unverified code on the host.

## Switch off and problems

Write `off` to `verify-mode`; the wrappers pass `--verify off` to the Go
commands. The `run.zsh.verify-off` / `checks.zsh.verify-off` scripts are from
older rollback sets. See the
[switch record](argon-implement-switch.md) for the sets and rollback steps.

If a run shows a problem, switch off with `verify-mode`, restore and check
the old configuration, and investigate before another attempt. For
a started run that does not count, the recovery marker blocks another run until
[manual recovery](argon-implement-run-recovery.md#manual-recovery).
For leftovers, follow the [sweep procedure](argon-verify-sweep.md).

The [smoke test][smoke] passed: `verify` verified a correct package;
`run_command` plainly and through a shell, an unlisted MCP tool, an extra
argument, `./pkg -v` and another MCP server were refused or unavailable.
Files matched and shared settings were unchanged.

[step1]: https://github.com/tkakkie/ribbitto/issues/1433#issuecomment-6303761064
[preflight]: https://github.com/tkakkie/ribbitto/issues/1433#issuecomment-6304130212
[smoke]: https://github.com/tkakkie/ribbitto/issues/1433#issuecomment-6304812132
