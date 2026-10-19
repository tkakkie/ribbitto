# Argon verification

The maintainer chose option B on 2026-10-19
([#1427](https://github.com/tkakkie/ribbitto/issues/1427), [spike part
2](https://github.com/tkakkie/ribbitto/issues/1427#issuecomment-6266769368)).
This rule awaits implementation.

## Status

The `verify` permission is **NOT in force**. It requires
[#1429](https://github.com/tkakkie/ribbitto/issues/1429),
[#1430](https://github.com/tkakkie/ribbitto/issues/1430),
[#1431](https://github.com/tkakkie/ribbitto/issues/1431),
[#1432](https://github.com/tkakkie/ribbitto/issues/1432) and this
documentation pull request to be merged, the extended real-path preflight in
[#1433](https://github.com/tkakkie/ribbitto/issues/1433) to pass and be
recorded, and the maintainer to explicitly approve enabling. After enabling
and a passing smoke test, a later small documentation change records the
permission as in force; this pull request does not make that change. The
Start gate and review independence stay. Until then Argon runs keep today's
rules byte for byte: no MCP, no `verify`, and settings retain the `mcp(*)`
deny.

## Tool permissions

Argon keeps no arbitrary shell, `run_command` or other command tool. MCP
admits only one fixed `verify` tool, through two agreeing layers:

- The guard allows `call_mcp_tool` only for the exact configured server,
  tool `verify` and strictly validated `Arguments`, sharing one Go schema
  with the server. Refuse extra, missing, unknown or duplicate keys, `null`,
  wrong types, over-long values, flags, absolute paths, `..` and disallowed
  path-like values. Check names and package patterns are fixed in code;
  no caller selects a command, worktree or image.
- Settings replace only the blanket `mcp(*)` deny with the narrow
  `mcp(<server>/verify)` allow when the feature is on. With it off, settings
  are byte-identical to today's, including all command and URL denies.
  The review launcher's settings and guard stay unchanged.

With MCP configured, the custom agent's `tools` list is no barrier:
`call_mcp_tool`, `list_resources`, `read_resource` and `manage_task` remain
(spike part 2). The guard allows seven file tools plus the one validated
call; every other tool, including the three resource/task tools, stays
denied. Headless mode auto-denied the spike's call with guard approval
alone; the settings allow is required too.

The feature switch, settings variant, guard mode and `mcp_config.json` must
agree before and after each run; any mismatch fails closed. A changed
settings, hooks or MCP file makes the run not count, as `denied_actions`
already does. Server identity, arguments and configuration bytes are fixed
in #1430/#1432 code.

## Trusted server

Outside `agy`'s sandbox, the MCP server is trusted computing base code. It
gets minimal credentials, Docker rights and host rights, with no interface
for arbitrary commands. `argonreview verify-mcp` offers one tool and only
calls `argonreview container-check`.

A fixed `mcp_config.json` starts it through `/usr/bin/env -i` with fixed
variables, a trusted binary path and fixed start-up arguments for the
trusted source path and worktree. Neither agy's environment nor a tool
argument supplies these. The server refuses to serve unless its build is
from the trusted revision, checked against that source. The MCP file and
server are included in pre- and post-run checks.

The server checks a fixed environment allowlist, settled by preflight. The
runner uses only constants and fixed Docker variables. `FAILED` and `NOT
VERIFIED` results set `isError`.

## Verification container

The runner uses no network: no outbound or loopback reach, including the
host gateway and Docker Desktop VM. The spike's loopback probe logged no
request under `--network none` ([part
1](https://github.com/tkakkie/ribbitto/issues/1427#issuecomment-6266603639)).
It runs on an isolated, throwaway source copy, never the original tree:

- Copy tracked and untracked non-ignored files with the hardened git
  settings from the [boundary](argon-implementer.md#the-boundary). Ignored
  files are excluded; copy caps are trusted constants.
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
  an unprivileged user, and tmpfs for `/tmp` and container `HOME`. Pin the
  image by digest with exactly the Go toolchain the copy's `go.mod` names;
  refuse a mismatch. Modules are offline with a read-only module cache:
  `GOPROXY=off GOTOOLCHAIN=local GOFLAGS=-mod=readonly CGO_ENABLED=0`.
- Time, memory (with equal swap limit), processes, CPU and output limits
  are constants in trusted code; no argument or environment changes them.
  Timeout, signal or abnormal exit removes container and copy; capped
  output is never verified.

Checks are exactly `go build <pkg>`, `go vet <pkg>` or `go test -count=1
-json <pkg>` for named packages admitted by the code's strict allowlist
pattern: no flags, spaces or `..` segment in the package argument, and no
other flags. The race detector and cgo are not run there; output says so,
and cgo-dependent packages stay CI-only.

## Results

The first line is `verified`, `FAILED` or `NOT VERIFIED`. Build/vet use zero
exit status; tests use per-test JSON, never package `ok` alone:

- A skipped test is never a success. Count leaf tests only: a passing
  parent whose subtests all skipped counts as skipped. Verification needs
  at least one passing leaf test, no failure or skip, and a final result
  for every targeted package.
- Zero passing tests, no test files, missing results, malformed or truncated
  output, a non-zero exit, an out-of-memory kill or abnormal exit are never
  `verified`. Any failure gives `FAILED`; any skip gives `NOT VERIFIED`
  with the skipped count and names.
- Database and browser tests are CI-only for now and appear as
  `NOT VERIFIED` with skipped test names. A test needing an omitted file
  fails or skips and is not verified in the container.

`verified` is Argon's own check; it never replaces green CI, review or
[handoff checks](running-other-ai.md#handoff-of-another-ais-code). CI stays
the check. Mutation evidence stays on scratch pull requests: #1429 excludes
moving it into the container.

## Extended preflight

[#1433](https://github.com/tkakkie/ribbitto/issues/1433) adds these checks
to the [existing preflight](argon-implementer.md#preflight), retained in
full. Claude runs the real Argon → MCP → Docker → test-result path on the
host, in a throwaway repository with the dedicated worker home:

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
feature-on settings, guard mode and MCP file. At the end, success or
failure, restore and check the feature-off settings, hooks and MCP file byte
for byte, the agent directory and `brain/` as before, no remaining session
state or session process. Record restoration too.

The evidence comment records every command and log, agy version, trusted
commit SHA, binary hash, image digest and identifiers (hashes) of guard,
settings and MCP configuration. List tools run and refused; for each
refusal, its layer (guard, settings, server or container), any side effect
or Docker start; each probe's pass/fail and whether its run counts. Record
no secret or home content; grep the record for the home path, sign-in file
names and credential variable names and require no matches.

Only after every preflight item passes and the maintainer explicitly
approves, switch normal operation on and record a minimal smoke test:
`verify` succeeds on a correct package; `run_command` and shell execution,
an unlisted MCP tool and an extra argument are refused; settings, hooks and
MCP file match the expected files; the account's shared settings hash is
unchanged; nothing unexpected changed. If any smoke check fails, switch off
immediately, restore and check the old configuration, and investigate before
another attempt. After a passing smoke test, make the later Status change
described above; the Start gate then points to #1433's record.

## Control code in Go

The direction is Go in `cmd/argonreview`, built from the trusted revision:
the implementer guard (`argonreview implement-guard`,
[#1431](https://github.com/tkakkie/ribbitto/issues/1431)), pre- and post-run
checks (`argonreview implement-check`,
[#1434](https://github.com/tkakkie/ribbitto/issues/1434)), run command
(`argonreview implement-run`,
[#1435](https://github.com/tkakkie/ribbitto/issues/1435)), container check
and verify server. These implementations are pending.

The Python guard and zsh scripts remain in use until each stage of
[#1436](https://github.com/tkakkie/ribbitto/issues/1436) is compared and
verified before use, with rollback: first the guard, then checks and run
command. Each stage keeps the full preflight and no weaker boundary; a
failed comparison, preflight or restoration stops the next real run.
Unverified code never runs on the host. Only after the staged switches are
the scripts reference only, with no Python in the run path. #1436 keeps MCP
off and its file empty.

If option B cannot be built, the maintainer's fallback is option C: the
launcher runs the checks between runs.