# Argon test output aggregation

#1850 is merged but **not in force until the production launcher is updated**
(#1700). Claude runs the post-merge preflight after the two-home swap is
authorised; only enabling needs separate maintainer approval.
[Verification](argon-verify.md), [test-db and summary](argon-verify-testdb.md),
[browser updates](argon-verify-browser-updates.md).

## One record

`test`, `test-db` and `test-browser` stream test2json into
`containerAggregator` in `cmd/argonreview/container_aggregator.go`. One function classifies checks. Events are checked on
arrival; stdout is never captured as a whole. Passing output is dropped
at its result, after checking failure markers. Streams share a byte cap
and a mutex. After a stop, writers acknowledge and discard bytes
without parsing or allocating. Cancellation stops the runner's child context;
it cannot masquerade as parent cancellation or erase the run's own deadline.

After container/source cleanup, the runner calls `finish` once to seal the
`containerRecord` in `cmd/argonreview/container_record.go`, using
completion/interruption at runner return and later deadline/parent state.
Verdict, host report, MCP summary and future #1847 logs share its reference.
Execution drops aggregator/protocol maps; no second parse.

The record holds completeness, first stop/event boundary, exact package/leaf
counts, observed failures, allowed/disallowed skips, retained failure/package/
compiler output, stderr/omitted bytes and first panic/timeout lines.
The failed/skipped leaf and failed-package index is first-come under entry/name budgets.
Names pin no decoder buffers or protocol maps. Optional skip omissions say `skip
names not retained`; memory-ceiling retention need not be deterministic. Browser
evidence separately keeps named result, outside-package/test flag, exact leaf
count and at most `browserLeafLimit + 1` leaf names/results under the same
ceiling (at most 65 identities).

Host `container-check` prints verdict, scope, then record, quoting untrusted
identities/diagnostics. Storage loss prints `truncated: N bytes not retained`;
exhausted shared output storage also prints `not retained`. Host prints retained
stderr even when not servable to #1847: Docker statuses 125–127, runner error,
cancellation or timeout can expose Docker-client paths. MCP budgets: 2,048 bytes
verified; 7,000 including wrapper/error reserve for errors; 30 names; 8 excerpt
lines of 200 bytes; 16 generated-diff lines.
Incomplete streams list `Failures (observed, total unknown):`.
Both print `Observed failures not named (not retained): N`, even with zero
retained names. MCP reserves the footer notice after names.
`and N more` follows its header. It and `Omitted: ... names not shown` count only
retained display omissions: shown + display-omitted + not-retained = observed failures.
With no names shown, MCP uses `<header> N more not shown`, or footer accounting
if it cannot fit. Host has no continuations; complete streams otherwise stay unchanged.
Totals need EOF with all results. Counts: `cut` for caps, kills or truncated
tails; `incomplete` otherwise. #1846's timeout body is fixed.
D5/Details-unavailable MCP bodies stay minimal. D1/test-browser late deadlines
keep `NOT VERIFIED: time limit exceeded` even with browser scope refusal.

`build`, `vet`, `generated` and write-back keep the command writer's 4 MiB
combined cap. Build/vet/generated copy a 16 KiB stdout prefix into the record.
Build/vet release buffers after rendering; generated keeps whole stdout for
diff/stat. Write-back keeps its archive. Image, recipe, guard and MCP schema
stay.

## Limits and retention

Values come from the approved [M1–M3 measurement][measurement] (2026-10-28).
Hard caps stop interpretation,
cancel execution and forbid `verified`. Soft limits only omit diagnostics;
they change no verdict, count or allowlist decision.

| Limit | Kind | Value and measurement rule |
|---|---|---|
| Both streams, total | hard | 32 MiB (33,554,432): next power of two at least twice 15,634,407; floor 16 MiB, above 128 MiB requires reconsideration |
| One line, either stream | hard | 256 KiB; measured maximum 750 bytes is below the 64 KiB reconsideration threshold |
| Explicit test runs | hard | 32,768; at least twice 15,038 = 30,076 |
| Packages | hard | 4,096; at least twice 49 |
| Accounted retained memory | hard | 16 MiB; twice measured name bytes plus peak running output gives 1,655,618 bytes; preflight RSS must not exceed 37,339,136 bytes |
| Run time, including preparation | hard | 10 minutes; measured 317–329 seconds |
| One identity / event fields | hard | 4,096 bytes / 32 fields; per-line consistency bounds |
| One failing test's output | soft | 32 KiB, head and tail halves; measured largest output 7,038 bytes |
| Failure and package output together | soft | 1 MiB; measured failed-output maximum 59,506 bytes |
| Stderr retained | soft | 256 KiB; twice measured 444,159 exceeds this ceiling, so the approved “at most” bound decides; cut is explicit |
| Index entries / name bytes | soft | 1,000 / 256 KiB; measured maxima 629 / 54,317 |
| Command stdout prefix | soft | 16 KiB; command capture/diff behavior above remains |

Charge every retaining allocation first: buffer capacities, replacements while
old buffers live, cloned identities, map overhead, index storage, browser
evidence and reserved allowlist slots. Decoder temporaries are bounded by one
line/field cap; heap tests check constants. #1847 record bound: failure output +
stderr + name bytes + entries × entry charge + command prefix + bounded browser
evidence + constant (about 1.5 MiB plus entries and at most 65 identity-length
browser names). Passing noise/protocol maps do not survive.

Malformed JSON, invalid UTF-8, raw NUL, duplicate/case-folded keys, unknown
actions and impossible states fail closed. Unknown fields such as Time, Elapsed
and FailedBuild and escaped NUL remain accepted. Partial last lines and missing
results refuse verification. Allowlisted skip messages use reserved per-test
storage outside the output pool; unconfirmable messages stay disallowed, even
with soft storage loss.

## N1 verdict precedence

First matching row wins. A confirmed failure is a newline-terminated `fail`
or `build-fail` event that passed all per-line and structural checks before
the first stop. A cap-crossing line or partial last line confirms nothing.
Panic/timeout output markers alone confirm no failure on an incomplete stream.

| Row | Situation | First line / body |
|---|---|---|
| Source | Source cleanup failed | `FAILED: source cleanup failed`; caller's existing body, before every row below |
| A1 | Confirmed failure then total cap, no deadline | `FAILED: output limit exceeded after a confirmed failure (output cut off)`; cap name and observed failures, no total |
| A2 | A1 with line, test/package count, memory, identity or field cap | A1, with the reached cap named |
| A3 | Only failure is in crossing/partial line | `NOT VERIFIED: output limit exceeded (output cut off)`; no observed failure |
| A4 | Confirmed package fail or build-fail then cap | A1 |
| A5 | Only panic marker then cap | A3; retained panic diagnostic |
| B1 | Total cap, no confirmed failure | A3; counts unavailable, cap named |
| B2 | B1 with each other cap | B1 |
| B3 | Only skips then cap | B1 |
| C1 | Cap plus run deadline, either order, no failure | B1; cap precedes deadline |
| C2 | C1 with confirmed failure | B1 plus observed list; #1857's first line wins |
| D1 | Completed passing stream, late deadline | `NOT VERIFIED: time limit exceeded`; fixed minimal body |
| D2 | Completed failing stream, late deadline | Own failing verdict |
| D3 | Completed skips/no tests, late deadline | Own `NOT VERIFIED` verdict |
| D4 | Completed exit 125, late deadline | Existing `FAILED`, minimal body |
| D5 | Deadline while running, with/without confirmed failure | `NOT VERIFIED: time limit exceeded`; fixed minimal body; observations available only to future #1847 index |
| E1 | Kill/exit 137 mid-stream with confirmed failure | `FAILED`; counts unavailable, observed list |
| E2 | E1 without confirmed failure | `FAILED`; counts unavailable |
| E3 | Exit 0, partial last line, earlier confirmed failure | `FAILED: empty or truncated test stream`; observed list |
| E4 | Exit 0, EOF, missing package result, confirmed failure | `FAILED`; observed list |
| E5 | Malformed event | `FAILED: <reason>`; earlier observations retained; invalid UTF-8 uses `malformed test stream` |
| E6 | Parent cancellation while running | Existing `FAILED`, minimal MCP body; aggregator's own cancellation is excluded |
| E7 | Runner error or exit 125–127 | Existing `FAILED`, minimal MCP body; stderr not servable |
| F | Complete stream, no stop | Oracle verdict and bounded summary; only a full pass is `verified` |

After source cleanup: cap, running deadline, first malformed line, then
exit/completed-stream rules including late deadlines. Unconfirmed container
removal refuses success; its independent timeout is not a run deadline. All
non-`verified` results set MCP `isError`. The legacy parser remains only as the
test differential/fuzz oracle. Approved `garbage\n{` gives E5's earlier
malformed line precedence over a truncated tail; both parsers fail.
Missing-result reasons use deterministic package order. The 4,097-byte-name
fixture is a **stricter** per-line refusal shared by host and MCP.

## Trust and changing a limit

Stdout cannot be authenticated within one uid and one container: a test can
write a valid test2json line through `/proc/1/fd/1`. Structural rules check
consistency, not proof. `verified` is Argon's own check, never a security
control. CI, review and the [handoff checks](running-other-ai.md#handoff-of-another-ais-code)
remain the check. Consistent forgery can pass.

Limit changes need one small PR in `cmd/argonreview/container_limits.go`, this
table and constant-derived boundary tests. Preflight: hard caps below/at/above
(including confirmed failure before crossing and crossing-line failure); soft
retention and omission markers; allowlist decisions with exhausted output. Memory:
heap accounting and runner peak RSS against baseline. Timeouts: D1–D5, cap
precedence, parent cancellation. Browser storage: leaf-limit/outside-scope
corpus. Run the differential oracle/fuzzer and mutation assertions for the
changed branch.

For post-merge host preflight, Claude uses a trusted rebuilt binary after the
authorised temporary two-home swap. Record on #1700: `test-db ./internal/web`
(counts/RSS within baseline), templ-rename failure (names), 2 GiB printer and
sweep, failure then flood (A1), forged valid stdout, repeated stage A
test/test-db/memory/PID probes. Record result bytes and agy's inline display.
This PR runs no probes or launcher update; enabling needs separate maintainer
approval.

[measurement]: https://github.com/tkakkie/ribbitto/issues/1850#issuecomment-6449085912
