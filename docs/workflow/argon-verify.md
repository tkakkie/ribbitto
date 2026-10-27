# Argon verification

Option B (2026-10-19)
([#1427](https://github.com/tkakkie/ribbitto/issues/1427), [spike part
2](https://github.com/tkakkie/ribbitto/issues/1427#issuecomment-6266769368)).
[Generator checks](argon-verify-image.md), [test-db](argon-verify-testdb.md) and the [summary](argon-verify-testdb.md#result-summary)
are in force since 2026-10-27 (#1700 stage A): production pins `argonreview-2e3a6bf`.
[Write-back](argon-verify-writeback.md) awaits stage B.

[Leftover sweep](argon-verify-sweep.md).

## Status

The `verify` permission is **in force since 2026-10-21**. Production uses
Go since 2026-10-22 (Stage 2). The maintainer approved enabling in
chat after reading [#1433]'s record. Evidence: [step 1][step1],
[preflight][preflight], [enabling and smoke test][smoke].
The Start gate and review independence stay. Only during a run, feature-on
settings replace the blanket `mcp(*)` deny; the home returns
to feature-off at rest. See [operation](argon-verify-operation.md).

## Tool permissions

Argon keeps no arbitrary shell, `run_command` or other
command tool. On-mode MCP admits one fixed `verify` tool through two agreeing
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

In [preflight][preflight], the guard refused every invalid call first;
the settings and server layers were not probed separately.

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

Outside `agy`'s sandbox, the MCP server is trusted computing base code with
minimal credentials, Docker and host rights, and no arbitrary-command
interface. `argonreview verify-mcp` offers one tool and only runs `container-check`.

A fixed `mcp_config.json` starts it through `/usr/bin/env -i PATH=/usr/bin:/bin`
with a trusted binary and fixed source and worktree arguments. Neither agy's
environment nor a tool argument supplies these. It serves only if its build matches the trusted revision in that source.
Pre- and post-run checks include the MCP file and server.

The server environment allows only `PATH`. Docker connection settings
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
  built image ID and exact Go, templ and sqlc versions; refuse mismatches.
  Modules are offline with a read-only module cache:
  `GOPROXY=off GOTOOLCHAIN=local GOFLAGS=-mod=readonly CGO_ENABLED=0`.
- Time, memory (with equal swap limit), processes, CPU and output limits
  are constants in trusted code; no argument or environment changes them.
  Timeout, signal or abnormal exit removes container and copy; capped
  output is never verified.

After generation: `go build -o /dev/null <pkg>`, `go vet <pkg>` or
`go test -count=1 -json <pkg>`. Packages admit no flags, spaces or `..`.
The `generated` check compares output and formatting in a copy; no write-back.

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
  with the skipped count and names, except `test-db`'s [measurement skips](argon-verify-testdb.md#limits-and-skips).
- Browser tests stay CI-only. Omitted files still fail or skip.

`verified` is Argon's own check; it never replaces green CI, review or
[handoff](running-other-ai.md#handoff-of-another-ais-code).
Scratch pull requests retain mutation evidence, excluded from the container by #1429.

## Extended preflight

Extend the [existing preflight](argon-implementer.md#preflight), keeping every
refusal. Claude runs the real Argon → MCP → Docker → test-result
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

Only during preflight, the managed temporary configuration uses #1432's
feature-on settings, guard mode and MCP file. Always restore and check feature-off settings, hooks and MCP file byte for byte,
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
unchanged; nothing unexpected changed. If any smoke check fails, switch off immediately,
restore and check the old configuration, and investigate before retrying. [Passing smoke test][smoke]; Start gate: [#1433]'s [preflight record][preflight].

## Control code in Go

Use Go in `cmd/argonreview`, built from the trusted revision: guard
(`implement-guard`, [#1431]), checks (`implement-check`,
[#1434](https://github.com/tkakkie/ribbitto/issues/1434)), verify server
(`verify-mcp`), container check (`container-check`), sweep (`container-sweep`)
and `implement-config`. Production also uses `implement-run`
([#1435](https://github.com/tkakkie/ribbitto/issues/1435),
[#1483](https://github.com/tkakkie/ribbitto/issues/1483)).

Production hooks use the Go `implement-guard` ([#1436] Stage 1, done
2026-10-20); the Python guard is reference only. [Stage 2](argon-implement-switch.md)
switched checks and the run command to Go on 2026-10-22, with comparison
and a rollback drill recorded. The zsh scripts are now thin wrappers.
Each stage keeps the full preflight and no weaker boundary; a failed
comparison, preflight or restoration stops the next real run. Unverified
code never runs on the host. Stage 2 is independent of the enabling
decision; operation must preserve the `verify-mode` switch,
pinned generated settings and hook hashes and pinned trusted source,
the post-run check before restore, and feature-off files at rest.

If option B cannot be built, the maintainer's fallback, option C, runs
checks in the launcher between runs.

[#1431]: https://github.com/tkakkie/ribbitto/issues/1431
[#1433]: https://github.com/tkakkie/ribbitto/issues/1433
[#1436]: https://github.com/tkakkie/ribbitto/issues/1436
[step1]: https://github.com/tkakkie/ribbitto/issues/1433#issuecomment-6303761064
[preflight]: https://github.com/tkakkie/ribbitto/issues/1433#issuecomment-6304130212
[smoke]: https://github.com/tkakkie/ribbitto/issues/1433#issuecomment-6304812132
