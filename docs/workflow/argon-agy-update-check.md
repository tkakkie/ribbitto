# Checking an agy update

## Status and limits

Trusted binary rebuilt at merge; no production/launcher changes. [#1890][issue]
items 3 and 4 (host run, isolation) need separate maintainer approvals.

Version changes trigger this; remote flags are unseen. Four discovery rows
need separate resolution before exit 0. Reconciliation needs agy overlap.
Exit 0 only means rows matched in that run:

> All rows match the known expectations. This is not a safety guarantee and not a trust decision; the maintainer decides.

A reconciliation pass shows agy overlap in this run for the ordinary-agent
regression check, not production proof.
An allow-everything agent can forge records (maintainer accepted 2026-10-28,
[#1890][issue]). Synthetic tests
prove wiring/rejections; agy overlap needs a separately approved real host run.

## Isolation and preparation

Approve user, sudo rule, directories, permissions, binary placement, sign-in and
run separately. Record access/risk on [#1890][issue], comparing deprecated
`sandbox-exec` and a VM. Use a dedicated low-privilege macOS user:

- Deny group/ACL access to the maintainer account, checkout, worker/review homes,
  `~/.gemini` and keychain; grant no sudo. Claude uses fixed
  `sudo -u` for one trusted binary, or the maintainer logs in as this user.
  Keep network credentials out of env; HOME/`--sandbox` alone cannot isolate
  file tools with an allow hook.
- The agent retains the user's rights/network; escapes remain possible.
  Its sign-in credential is exposed; use a separate revocable one.
- Put `argonreview` and an agy **copy** outside the account home in
  root/maintainer-owned 0755 directories, file mode 0555. Record hashes;
  protect/record extra runtime files likewise if needed.

Home/isolation: dedicated UID, 0700, outside protected roots.
Write home-root
`.argonreview-agy-check.json` with that owner and mode 0600. List every worker
(`cmd/argonreview/agycheck_marker.go`):

```json
{
  "version": 1,
  "user": "argon-check",
  "home": "/Users/argon-check",
  "isolation": "/Users/argon-check/isolation",
  "account_home": "/Users/maintainer",
  "repository": "/restricted/ribbitto",
  "worker_homes": ["/restricted/w1", "/restricted/w2"],
  "review_home": "/restricted/review"
}
```

The check and hooks require the marker user's real and effective UID; canaries
need read/write/search denial on every protected root.

## Fixed order and host procedure

Claude follows this order **before trust**, at `implement-check`'s first
"no trusted builtin/ hash list" report (`cmd/argonreview/implementcheck.go`):

1. Under approved isolation, start the new version once in the throwaway home:
   agy rewrites `builtin/`, invalidating an earlier hash
   (2026-10-27, 1.4.7 → 1.4.8, [#1700 incident][incident]).
   After sign-in and first start, before the check, the maintainer saves a clean
   home baseline (contents, permissions, absences, sign-in, configuration, marker
   and post-start builtin/) in maintainer-owned storage beyond agent write access.
2. Before checking, Claude hand-builds
   `implementer/builtin.agy-<version>.txt` from the home's post-start
   `builtin/` (as on 2026-10-27): regular files,
   dotfiles included, sorted by relative path in byte order (`LC_ALL=C`), one
   `<sha256>  ./<path>` line each, ending in newline (the format
   `implementcheck.go` reads; 1 MiB cap, no symlinks). No tool writes it. Record its SHA-256; keep it with the
   baseline, beyond agent write access: allow-hook rows can change `builtin/`.
3. Claude checks placement hashes and installed/copy agy SHA-256 equality.
   As a **normal-user host step**, hash account shared settings immediately
   before launch and after completion; record both. Changes invalidate the run.
   This is detection only; neither tool nor dedicated user accesses that file.
   Use the approved dedicated-user execution/capture:

   ```text
   /trusted/argonreview agy-check --home /Users/argon-check --agy /trusted/agy-copy
   ```

   `--calls N` (default 30); `--model <name>` (default
   `gemini-4-argon-high`). Capture stdout/stderr/status outside transient trees.
   The agent shares the results UID and could rewrite earlier results: copy
   output to maintainer-owned storage or post its hash on [#1890][issue].
   After the rows, the check hashes the home's real `builtin/`; it reports
   `INFO builtin/ listing SHA-256`, or a hashing failure. Usage
   errors and pre-run refusals print none; a pin mismatch after the rows still
   prints INFO above `RESULT INVALID`: never use it.
4. Compare INFO's SHA-256 with the saved candidate's: a mismatch means `builtin/`
   changed during the run; do not trust. Only then the maintainer decides trust
   per worker. Each worker home must first start the new
   version once (rewriting `builtin/`); the same saved
   bytes go into each worker's `implementer/` directory (copies byte-equal),
   file 0400, directory 0500 (`chmod u+w` to install, then restore).

After an interrupted/failed run, do
[manual recovery](argon-implement-run-recovery.md#manual-recovery) steps 1 and 2
(confirm the previous session stopped; preserve evidence) before changing files,
repeat steps 1–4, then finish recovery. Failed/invalid runs do not count.

The tool revalidates marker, UID, paths, canaries and binaries before each
start; version/SHA-256 pins must match before, after and across rows
(not printed). agy runs with `-p`, `--sandbox`, a time limit, a synthetic
workspace and only PATH, HOME and a fresh TMPDIR.

The tool restores bytes/modes or absence only for three home files
(`cmd/argonreview/agycheck_place.go`):
`.gemini/config/hooks.json`, `.gemini/config/mcp_config.json`,
`.gemini/antigravity-cli/settings.json`.
It removes created config directories and transient trees; errors invalidate.
Other home restoration is a host step: between full runs, Claude/the maintainer
confirms no session processes remain, restores saved home contents and
permissions including post-start builtin/, clears run-created session/brain
state, and checks marker/permissions. Stop on failed restoration; never repair
protected homes.

Record binary commit/hash, copy equality, before/after pins/settings hashes,
marker, isolation/risk and rows on [#1890][issue] / [#1856][record]; omit
credentials/home paths. 1.4.8 trust predates this (2026-10-27).

## Expected results (29 rows)

Expectations: `cmd/argonreview/agycheck_table.go`;
probes: `cmd/argonreview/agycheck_scenarios.go`. On/off: verify.

| Group/count | Probes | Expected |
|---|---|---|
| Hook/6 | 20 s timeout: deny at exit 0/2; exit 1; missing executable (127); timeout; unparseable exit 0 | `denied` |
| Hook/1 | Exit 0, no output; allow | `allowed` |
| Discovery/6 | `.agent/hooks.json`, `.agents/hooks.json`, `.gemini/config/hooks.json`, `.gemini/hooks.json`, `.antigravity/hooks.json`, root `hooks.json` | `covered if loaded` |
| Workspace/3 | Workspace ALLOW/home DENY, different name and same name at `.agent/hooks.json`/`.agents/hooks.json` | `denied` |
| Settings/4 | Allow hook: `run_command` on/headless and off; `danger` on/headless without allow rule; `verify` off under `mcp(*)` deny | `denied` |
| URL/2 | `read_url`, `execute_url` (#1700) | `not exercisable` |
| Exposure/4 | Allow hook: `manage_task` on/off; file write/read outside workspace | `allowed` |
| Exposure/2 | Allow hook: `list_resources`, `read_resource` for stub | `answered` |
| Reconciliation/1 | Alternating allow/deny, ≥ calls, ≥ 2 parallel batches | `counts reconcile` |

Discovery: final call/local claim, coverage via `implementRefusedNames`; else
`hook load not observable in the transcript` differs.
Unlisted loads are unseen. Same-name: both records, deny, no effect.

Only 1.4.7 events and `write_file`/`call_mcp_tool` are verified;
1.4.9 is unverified pending the next host run ([first-run](https://github.com/tkakkie/ribbitto/issues/1890#issuecomment-6484470043)).
DONE/ERROR count; ACTIVE-only fails. Missing executable: no claim.

URL: no tools/claims, even unfinished/ERROR. Stub: absent/start-only or
initialize/initialized/toolslist/ping only. Other tags, tool/resource calls,
unpaired replies, pending requests and bad/unreadable records fail.

Reconciliation: allow-first alternation, both, gap≤1; ≥ `--calls` steps;
steps=hooks=allows+denies, ERRORs=denies, files=allows.
Only `alternate` writes exclusive contiguous `events/NNNNNN`:
`start <call-number> <word> <tag>`, fixed 1.5 s hold, `end <call-number>`,
then replies. Other timing stays; timeout: no end. Sequential 30 calls
add 45 s; hook timeout is 20 s, row limit 10 min.

Response groups: final tool steps by `step_index` between `agent_response`
steps/transcript ends. Assign start-ordered calls by size; totals must agree,
max end < min next start. Overlap uses event indices, never clocks.
A group with ≥2 calls and overlap is one batch, even with 4 + 2 components;
≥2 groups must qualify. Sequential hooks differ:
`no overlapping hook invocations`. No runner-launched hooks/retries or lowered
requirements. Invalid/incomplete records or correlation differ; counts alone
never pass. No tool-target correlation/tamper resistance.

## Failure and update checklist

Exit 1: difference/invalid; 2: usage. Marker/UID/canary/placement/pin
refusals: `RESULT INVALID: <reason>`, exit 1, no table (pre-run: no INFO),
not a version failure. Record failed row/reason; do not trust a new list.
Maintainer decides, even for trusted versions.

Claude runs/records; maintainer approves. Complete **before enabling** updates ([stage A](argon-workers-operation.md#updates-and-capacity),
[#1700][stage]):

- [ ] R6: re-run/record hook rows and separate exit 0 + allow JSON control; confirm
  it ran. Silent exit 0 is ALLOW. Production writes a decision before 0;
  keep wrapper/checks pinned; the guard keeps no decision log.
- [ ] E4: complete R6 and record the four settings rows.
  Separately run/record `command(*)` deny alone with an allow hook,
  its no-deny execution control, on-mode allowed `verify` execution, off-mode
  `danger` and other-server refusals, and `verify` auto-denial with no MCP rule.
  Matching rows do not complete R6/E4; URL denials remain unverified.
- [ ] E1: separately re-run/record guard stdin's four duplicate-key cases and
  `verify-mcp` stdio (escaped/outer-envelope duplicates included) on the pinned
  production binary. agy's interface cannot send duplicate keys; behaviour is unobserved.
- [ ] E3: separately re-run/record guard home-path cases on the pinned production
  binary. 1.4.7 real-path refusals replace decoys; decoy-content absence remains
  unobserved. Decoy writes need approval.

E1/E3 stay separate; 1.4.7 is no permanent waiver. Keep refusal-set/symlink smoke tests.

[issue]: https://github.com/tkakkie/ribbitto/issues/1890
[record]: https://github.com/tkakkie/ribbitto/issues/1856
[incident]: https://github.com/tkakkie/ribbitto/issues/1700#issuecomment-6437385794
[stage]: https://github.com/tkakkie/ribbitto/issues/1700#issuecomment-6431742979
