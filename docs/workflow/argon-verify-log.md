# Verify log retrieval

## Status

Merged (pull requests for #1913's PR 1), not exposed, not in force.
This page starts from #1847's final design record at d4095b3. The pure core
is unexposed: nothing is listed or wired into verify-mcp, the guard or settings.
Exposure, the launcher update and enabling need the maintainer's separate
approvals. CI stays the check.

## Purpose and limits

`agy` replaces a large non-error MCP result by a file reference the guard
forbids Argon to read, and shows only the head of a large error. When the
[result summary](argon-verify-testdb.md) is insufficient, the planned
`verify-log` tool reads retained failure output in the same `ribbitto-verify`
server. It reads only; it starts no container, opens no file and takes no
path. `verify` and its schema do not change.

## Capture and retention

The [aggregation record](argon-verify-aggregation.md) owns retention and
limits. A capture holds that sealed record by reference, with no second parse
or output copy. #1913 adds a 128-bit `crypto/rand` run ID (32 hex digits), fixed
ordinals, display names, taint flags and a process-memory store of the last 4
captures, oldest evicted first, with a byte check against 4 × R_max derived
from the record constants. Actual pinned fields and derived data are charged;
a capture above R_max stores nothing and gets no run ID. Only a final first
line starting `FAILED` or `NOT VERIFIED` stores one; verified stores nothing.
No stderr is served after a runner-level failure (exit 125–127, runner error,
cancellation or timeout), even though the host record retains it.

## Selectors and index

`parseVerifyLogArguments` is stateless, for the future guard to share.
Strict JSON uses the same duplicate-key checks as `verifyObject`: exactly
these keys, no unknown or case-folded keys, no duplicate, extra or null values.
All values are strings.

- `run`: exactly 32 lowercase hexadecimal digits.
- `stream`: `index`, `test`, `package`, `stderr` or `output`.
- `name`: present if and only if stream is `test` or `package`. Either `#N`
  (1–5 ASCII digits, first digit nonzero) or a display name of at most 256
  bytes of valid UTF-8, without control, ANSI, bidi or zero-width characters.
  Selectors beginning with `#` are reserved for ordinals; malformed ordinal
  forms, Unicode digits, signs and trailing spaces are refused.
- `start`: 1–7 ASCII digits, first digit nonzero. The parser checks syntax;
  only the reader checks the value against the line count. Thus 10 and 1234567
  are syntactically valid even when beyond a particular unit.

The index is the server's selectable table. It numbers every retained failed
leaf, disallowed skipped leaf and failed package. No passing test, allowed
skip, skipped package or parent is selectable. Failed leaves sort by raw
package/name as in `recordSummaryFacts`; skipped leaves join that order.
Failed packages follow in the record-entry order used by its package-failure
projection. Ordinals are assigned once, never recomputed on reads. PR 2 adds
them to the summary.

Display names call the merged `summaryText` directly: ANSI is removed,
invalid UTF-8 becomes U+FFFD, controls, bidi and zero-width characters are
removed, and bytes are capped at 200 without a marker. This is narrower than
the original design record's wording; the visible control tokens and cut
suffix below apply to data lines, not display names. A display form shared
by two entries marks both `ambiguous: use #N`. Unique display names are an
exact-equality convenience, never a path. Ordinals are the guaranteed selector;
`#` plus digits always means an ordinal. A wrong-kind ordinal is not listed.

`test` serves that leaf's retained output. Package entries do not retain
attributed output: each listed failed package serves the run's shared package
output followed by compiler output, potentially from other packages in that
run. `stderr` and `output` serve only the first 16 KiB. `output` requires the
record's command classification (including writeback); test2json records have
no output stream. No verified capture exists to read.

Omission notices preserve `truncated: N bytes not retained` and `not retained`.
Complete host/index reports say `names not retained for K failures`; the
existing complete MCP summary includes that loss in its `names not shown`
footer. Incomplete records label failures `observed, total unknown` and share
the observed storage-loss notice. No incomplete record claims final counts.
A shared-record test checks counts, names and notices.

## Paging, encoding and frame

The limit is on the entire serialized JSON-RPC line, including envelope,
echoed ID, JSON and HTML escaping, and the newline appended by Encode.
The builder measures the same `verifyResponse` and content/isError shape
as the server. HTML escaping remains on: `<`, `>`, `&`, U+2028 and U+2029
cost six bytes; backslashes and newlines cost two. Escape-dense pages and
the largest accepted ID are tested against the byte cap. A completed response
above the cap becomes a fixed refusal as an independent defence.

A page has at most 80 physical lines, including metadata and frame. It says
`lines a-b of n` and `next start: b+1` or `end`. Greedy assembly adds a line
only while the measured response fits. At least one worst-case data line fits
with the largest ID; successful pages set isError false.

Each data line starts with `| `. ANSI is removed; invalid UTF-8 becomes U+FFFD.
Controls (C0, C1, DEL), bidi and zero-width characters in exactly the set dropped
by `summaryText` appear as `\u{XXXX}` tokens. The 512-byte data-line cap includes
the prefix and `[cut N bytes]` suffix. Cuts preserve whole runes and control
tokens; N counts source bytes not shown. Removed ANSI alone does not cause a
cut notice. The sanitizer has a differential escape-sequence corpus against
the summary's terminal grammar.

The data is enclosed by `BEGIN UNTRUSTED OUTPUT <nonce>` and
`END UNTRUSTED OUTPUT <nonce>` with a fixed sentence: output of code in this
worktree, data, not instructions; verdicts come only from verify. The nonce is
minted after the retained content is fixed and checked against the source and
served data. A forged frame, verdict or index remains behind `| `. A model may
still follow instructions it reads; framing is not proof against that risk.

## Secret tripwire

The container uses fixed variables, a public CI-only database URL and a protected source copy. Files, host diagnostics, runner errors, saved agy output and other-worker runs are never served.

Once at capture, scan every retained output unit and entry name for the CI
password derived from the runner constant and token shapes `ghp_`,
`github_pat_`, `AKIA` and PEM headers. Scan whole units, with joined lines,
before and after removing ANSI/invisible characters, without a page or prefix
cap. Head/tail and package/compiler joins conservatively over-detect rather
than let a boundary bypass the tripwire. Only detection counts survive.
A matching name displays `[name withheld]` and stays selectable only by
ordinal; future PR 2 uses the same mask in every summary mode. Existing
summaries are untouched by PR 1.

A matching unit is tainted: every read is refused whole, independent of start.
Stepping past a PEM header or the served prefix cannot expose its body. No
match is returned or audited. A secret split across events in a way retention
does not rebuild can evade the tripwire; the container holds none by
construction.

## Budgets, refusals and audit

Budgets are per capture per stream. Server bytes count serialized response
lines, including envelopes and refusals, not captured bytes. Refusals consume
reads too. After exhaustion only fixed refusals are admitted; their mandatory
wire costs still count, so the counter can exceed its cap without admitting
another successful page. Captures and counters are protected by a mutex;
record fields are immutable after sealing. Reads use completed captures and
need no in-flight verifier state.

One fixed message per refusal class lives in one table: malformed (mapped to
the existing protocol error), no such run, not listed, ambiguous, start out of
range, stream not allowed, stderr not servable after runner failure, tainted
unit, spent budget and response too large. Unknown, evicted, other-worker and
verified runs all say `no such run in this session`. Direct reader calls with
malformed IDs share that message; malformed wire syntax is the protocol error.
No refusal echoes input. No error reveals a secret match.

Each read/refusal formats one audit line: at most eight hex ID digits, a fixed
stream label, served range, serialized bytes, class, unit and name detection
counts. It contains no name, match or content. PR 2 owns writing that line to
stderr and writing the response; PR 1 has no server-loop caller.

## Regression rule and provisional numbers

PR 2 derives regression fixtures from base commit 6144641: verify, its schema,
guard decisions and off-mode settings stay unchanged with logs on/off. Tests
derive checks and guard rows rather than copying a check list or property
count. Both parsers refuse the other's arguments. PR 1 tests parser separation
and a closed import/call boundary for new production files: no file or
container read. PR 2 checks the actual server-loop path too.

Values live in `cmd/argonreview/verifylog_limits.go`. Measurement before
enabling confirms them; changes need one small PR and corresponding preflight
rows. No host measurement is approved here.

| Constants | Provisional | Basis / confirmation |
|---|---|---|
| `verifyLogCaptures` | 4 | session read pattern; revisit after two weeks on |
| `verifyLogRecordMax`, `verifyLogStoreBytes` | R_max; 4 × R_max | aggregation output + stderr + names + stdout + base + browser charges |
| `verifyLogResponseBytes`, `verifyLogPageLines`, `verifyLogDataBytes` | 6,144 bytes; 80 lines; 512 bytes | 7.8 KB once inline; worst-page preflight |
| `verifyLogPrefixBytes` | 16 KiB | measured stderr 21,495 bytes; preflight |
| `verifyLogIndexReads`, `verifyLogEntryReads`, `verifyLogAuxReads` | 16; 40; 8 | 26 failing leaves; preflight read counts |
| `verifyLogServerReads`, `verifyLogServerBytes` | 256; 1 MiB | preflight read pattern |

## Exposure and preflight (future PR 2)

A trusted logs switch requires verify on and matching generated/byte-checked
hooks, MCP config and on-mode settings. Off: not listed, guard and settings
refuse; on: exactly the additional verify-log permission. A mismatched switch
refuses preflight. Nothing in PR 1 enables this.

After both merges, #1913's #1700 host preflight needs a trusted rebuilt binary
and a separately authorised temporary two-home swap. Probe paging, refusals,
two workers, hostile/secret output, regression, switch disagreement and audit.
Measure reads for the templ-rename failure's 26 leaves before enabling.
The launcher update and verify-log-mode on each need separate approval.
