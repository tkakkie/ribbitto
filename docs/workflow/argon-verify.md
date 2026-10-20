# Argon verification

Option B (2026-10-19)
([#1427](https://github.com/tkakkie/ribbitto/issues/1427), [spike part
2](https://github.com/tkakkie/ribbitto/issues/1427#issuecomment-6266769368)).
Implementation pending.

[Leftover sweep](argon-verify-sweep.md).

## Status

The `verify` permission is **NOT in force**. It requires
[#1429](https://github.com/tkakkie/ribbitto/issues/1429),
[#1430](https://github.com/tkakkie/ribbitto/issues/1430),
[#1431],
[#1432](https://github.com/tkakkie/ribbitto/issues/1432) and #1442
to be merged; [#1436] Stage 1 to be complete before [#1433]'s extended
real-path preflight (the real Argon run path, the hook that agy uses in normal
operation, already runs the Go guard built from the trusted revision, after
Stage 1's whole existing preflight passed through it and one real run
completed with it); that preflight to pass and be recorded; and the
maintainer to explicitly approve enabling.
#1436 Stage 2 is independent of enabling. After enabling and a passing smoke
test, a later small docs change records the permission as in force, not
#1442. The Start gate and review independence stay. Until then Argon runs
keep today's rules and settings unchanged: no MCP, no `verify`, the
`mcp(*)` deny stays.

## Tool permissions

(When in force) Argon keeps no arbitrary shell, `run_command` or other
command tool. MCP admits only one fixed `verify` tool through two agreeing
layers:

- The guard allows `call_mcp_tool` only for the exact configured server,
  tool `verify` and strictly validated `Arguments`, sharing one Go schema
  with the server. Refuse extra, missing, unknown or duplicate keys, `null`,
  wrong types, over-long values, flags, absolute paths, `..` and disallowed
  path-like values. Check names and package patterns are fixed in code;
  no caller selects a command, worktree or image.
- Feature-on settings replace only blanket `mcp(*)` deny with narrow
  `mcp(<server>/verify)` allow. Feature-off settings are byte-identical to
  today's, including all command and URL denies.
  The review launcher's settings and guard stay unchanged.

With MCP configured, the custom agent's `tools` list is no barrier:
`call_mcp_tool`, `list_resources`, `read_resource` and `manage_task` remain
(spike part 2). The guard allows seven file tools plus the one validated
call; every other tool, including the three resource/task tools, stays
denied. Headless mode auto-denied the spike's guard-approved call;
settings must allow it.

The feature switch, settings variant, guard mode and `mcp_config.json` must
agree before and after each run; any mismatch fails closed. A changed
settings, hooks or MCP file makes the run not count, as `denied_actions`
does. Server identity, arguments and configuration bytes are fixed
in #1430/#1432 code.

## Trusted server

Outside `agy`'s sandbox, the MCP server is trusted computing base code. It
gets minimal credentials, Docker and host rights, with no interface
for arbitrary commands. `argonreview verify-mcp` offers one tool and only
calls `argonreview container-check`.

A fixed `mcp_config.json` starts it through `/usr/bin/env -i PATH=/usr/bin:/bin`
with a trusted binary and fixed source and worktree arguments. Neither agy's
environment nor a tool argument supplies these. The server serves only if
its build matches the trusted revision, checked against that source.
Pre- and post-run checks include the MCP file and server.

The server allows only `PATH` in its environment. Docker connection settings
are constants in the runner; its source copy uses `/tmp`, ignoring `TMPDIR`.
`FAILED` and `NOT VERIFIED` results set `isError`.

## Verification container

The runner has no outbound or loopback network reach, including the
host gateway and Docker Desktop VM. The spike's loopback probe logged no
request under `--network none` ([part
1](https://github.com/tkakkie/ribbitto/issues/1427#issuecomment-6266603639)).
It uses an isolated, throwaway source copy, never the original:

- Copy tracked and untracked non-ignored files with the hardened git
  settings from the [boundary](argon-implementer.md#the-boundary).
  Copy caps are trusted constants.
  Omit root `.git`, `.github`, `AGENTS.md`, `CLAUDE.md`, `.gitattributes`
  and `.gitmodules`. Refuse nested `.git`, those baseline names outside
  the root and the guard's other refused names at any depth,
  case-insensitively. No protected files enter the copy.
- Refuse symlinks and non-regular files, never follow them while opening;
  check open descriptors, then file identity, size, modification time and
  the file set again; a change refuses the run. Remove the copy even on
  failure. Never copy or mount the dedicated `HOME`, sign-in token or
  parent `.git`.
- Use a read-only container root, dropped capabilities, no new privileges,
  an unprivileged user, tmpfs for `/tmp` and container `HOME`. Pin the
  image by digest with the exact Go toolchain in the copy's `go.mod`;
  refuse a mismatch. Modules are offline with a read-only module cache:
  `GOPROXY=off GOTOOLCHAIN=local GOFLAGS=-mod=readonly CGO_ENABLED=0`.
- Time, memory (with equal swap limit), processes, CPU and output limits
  are constants in trusted code; no argument or environment changes them.
  Timeout, signal or abnormal exit removes container and copy; capped
  output is never verified.

Checks are exactly `go build <pkg>`, `go vet <pkg>` or `go test -count=1
-json <pkg>` for named packages matching the code's strict allowlist
pattern: no flags, spaces or `..` segment in the package argument, and no
other flags. Output states no race detector or cgo runs there;
cgo-dependent packages stay CI-only.

## Results

First line: `verified`, `FAILED` or `NOT VERIFIED`. Build/vet use zero
exit status; tests use per-test JSON, never package `ok` alone:

- Skips never succeed. Count leaf tests only: a passing
  parent whose subtests all skipped counts as skipped. Verification needs
  at least one passing leaf test, no failure or skip, and every targeted
  package's final result.
- Zero passing tests, no test files, missing results, malformed or truncated
  output, non-zero exit, out-of-memory kill or abnormal exit are never
  `verified`. Any failure gives `FAILED`; any skip gives `NOT VERIFIED`
  with the skipped count and names.
- Database and browser tests are CI-only for now, shown as
  `NOT VERIFIED` with skipped test names. A test needing an omitted file
  fails or skips, never verified in the container.

`verified` is Argon's own check; it never replaces green CI, review or
[handoff checks](running-other-ai.md#handoff-of-another-ais-code). CI stays
the check. Mutation evidence stays on scratch pull requests, excluded from
the container by #1429.

## Extended preflight

For [#1433]'s preflight and once in force,
extend the [existing preflight](argon-implementer.md#preflight): keep current
items; replace only the MCP refusal with "MCP other than the
exact `verify` call". Claude runs the real Argon → MCP → Docker → test-result
path on the host in a throwaway repository with the dedicated worker home:

- **Allow:** `verify` passes on a real package; a deliberately broken
  change makes the targeted test fail by name.
- **Deny:** `run_command` plainly, through a shell and in the background;
  `danger`-style unknown tools; another MCP server; `verify` with a flag,
  path or extra argument; `manage_task`, `list_resources`, `read_resource`;
  and all older refusals: outside reads/writes, refused names, the home,
  URL/browser tools, `search_web`, subagents, `generate_image`, `schedule`,
  a failing guard, commit, push, `gh` and other AI CLIs. Each probe passes
  only when its refusal shows and its effect is absent. Record each
  probe's result separately from whether its run would count: the
  [launcher's disqualifiers](running-argon.md#what-a-review-does), including
  `denied_actions`, non-zero exit and changed tree, stay unchanged.
- **Boundary from inside the container:** the loopback probe logs no
  request; no dedicated `HOME`, sign-in token, parent `.git` or protected
  file is visible; a test writing into the copy leaves the worktree
  unchanged; the server environment has no forbidden variable.
- **Skips:** a database-backed package reports `NOT VERIFIED` and its
  skipped test names, never `verified`.

Preflight's temporary permission is not the normal-operation permission.
Only during preflight, the managed temporary configuration uses #1432's
feature-on settings, guard mode and MCP file. After success or failure,
restore and check feature-off settings, hooks and MCP file byte for byte,
the agent directory and `brain/` as before, no remaining session state or
session process. Record restoration.

The evidence comment records every command and log, agy version, trusted
commit SHA, binary hash, image digest and guard,
settings and MCP configuration hashes. List tools run and refused; for each
refusal, layer (guard, settings, server or container), any side effect
or Docker start; each probe's pass/fail and whether its run counts. Record
no secret or home content; grep the record for the home path, sign-in file
names and credential variable names and require no matches.

Only after all preflight items pass and explicit maintainer approval,
switch normal operation on and record a minimal smoke test:
`verify` succeeds on a correct package; `run_command` and shell execution,
an unlisted MCP tool and an extra argument are refused; settings, hooks and
MCP file match the expected files; the account's shared settings hash is
unchanged; nothing unexpected changed. If any smoke check fails, switch off
immediately, restore and check the old configuration, and investigate before
another attempt. After a passing smoke test, make the later Status change
above; the Start gate then points to #1433's record.

## Control code in Go

Use Go in `cmd/argonreview`, built from the trusted revision:
the implementer guard (`argonreview implement-guard`,
[#1431]), pre- and post-run
checks (`argonreview implement-check`,
[#1434](https://github.com/tkakkie/ribbitto/issues/1434)), run command
(`argonreview implement-run`,
[#1435](https://github.com/tkakkie/ribbitto/issues/1435)), container check
and verify server. These implementations are pending.

Production hooks use the Go `implement-guard` ([#1436] Stage 1, done
2026-10-20); the Python guard is reference only. The zsh scripts stay until the
checks and run command are compared and verified, with rollback.
Each stage keeps the full preflight and no weaker boundary; a failed
comparison, preflight or restoration stops the next real run. Unverified
code never runs on the host. After the switches the scripts are reference
only. #1436 keeps MCP off and its file empty.

If option B cannot be built, the maintainer's fallback is option C: the
launcher runs the checks between runs.

[#1431]: https://github.com/tkakkie/ribbitto/issues/1431
[#1433]: https://github.com/tkakkie/ribbitto/issues/1433
[#1436]: https://github.com/tkakkie/ribbitto/issues/1436
