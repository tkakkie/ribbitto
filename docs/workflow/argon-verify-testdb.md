# Verify database check

## Status

`test-db` (#1697) is in force since 2026-10-27 (#1700 stage A, launcher
`argonreview-2e3a6bf`). Codex has no Docker; #1700's preflight validated
the Docker/PostgreSQL/script path. CI stays the check, alongside review and
[handoff checks](running-other-ai.md#handoff-of-another-ais-code).

## Fixed runner

The [runner](../../cmd/argonreview/container_testdb.go) shares `test`'s local
package-pattern validator; callers supply no flags, image, environment,
command or server settings. The pattern is positional:
`sh -c <fixed script> sh <pattern>`. The shared schema still has exactly
`check` and `package`, with no additional properties.

The POSIX sh script uses `set -eu` and:

1. Copies `/work/.` to `/tmp/work`, including dotfiles, and runs the shared
   templ, sqlc and Tailwind commands there. Generator logs go to stderr.
2. Runs `/usr/lib/postgresql/18/bin/initdb` with
   `-D /home/check/pgdata -U postgres -E UTF8 --locale=C.UTF-8 --auth=trust --no-sync`.
   It uses the libc `C.UTF-8` locale that the image has, not the
   [builtin provider](../database-locales.md): that page keeps the libc
   default for development, CI and headless-review setups so that they
   exercise libc deployments, and recommends builtin only for new clusters.
   CI's `postgres:18` uses `en_US.UTF-8`, so a collation-sensitive difference
   from CI is possible and CI stays the check. The data lives
   on the existing home tmpfs; initdb creates its directory with mode 0700.
3. Starts `/usr/lib/postgresql/18/bin/postgres` in the background with that
   data directory and exactly these settings: `listen_addresses=127.0.0.1`,
   `unix_socket_directories=/tmp`, `fsync=off`, `synchronous_commit=off`,
   `full_page_writes=off`, `max_connections=100`. The default port is 5432.
4. Tries `/usr/lib/postgresql/18/bin/pg_isready -h 127.0.0.1 -p 5432 -U postgres`
   at most 100 times with 0.1-second sleeps, stopping early if the server
   dies. Failure stops and waits for the server and exits 27 before Go runs:
   an empty stream and nonzero exit give `FAILED`.
5. Runs `go test -count=1 -json -p 2 "$1"`, captures its status, sends
   PostgreSQL SIGTERM, waits, then exits with Go's status. Cleanup also runs
   on shell errors after startup. `-p 2` is a named constant tested against
   the Makefile default, sized for 100 connections (#636). No race detector.

Initdb, server, readiness and generator logs go to stderr; only Go's JSON
stream goes to stdout. The image pin is [recorded on the image page](argon-verify-image.md#runner-pin-and-prelude).

Only this check adds the trusted constants `containerTestDatabaseURL`
and `containerRequireDB`: `RIBBITTO_TEST_DATABASE_URL` gets CI's public test
URL, and `RIBBITTO_REQUIRE_DB=1` requires the database. The URL's password
is public and CI-only; trust authentication ignores it. Neither caller
arguments nor host environment supplies these values. The Docker host
environment is unchanged. Browser variables, `CI` and measurement gates
are never set by the runner.

## Limits and skips

Container limits remain 2 GiB memory with equal swap, 256 PIDs, 2 CPUs,
10 minutes and 1 GiB per tmpfs. Test streams use [aggregation](argon-verify-aggregation.md):
merged but not in force until the production launcher is updated (#1700).
Its limits replace the combined capture cap; PostgreSQL stderr counts toward
total streamed bytes, with a separate soft retention limit. Hard stops never
verify. The [timeout line](#timeout-and-cancel-results) is unchanged.
Browser tests, cgo-dependent packages and race checks remain CI-only.

#1700 stage A phase 1 observed on the unchanged repository: `test ./internal/web` produced
3,844,400 bytes (below the cap of 4,194,304, with no container standard error);
`test-db ./internal/web` (database tests run too, plus the generators' and
PostgreSQL's logs on standard error) gave `NOT VERIFIED: output limit exceeded`
once, in a run with the encoding patched around the bug, and `test-db './...'`
the same; how that total splits between test output, generator logs and
PostgreSQL logs was not measured.
A package whose directory has no test files counts as a skip, so a `./...`
or `./dir/...` pattern that includes such a package is `NOT VERIFIED`
(give one package per call).

Only this check accepts the following measurement skips. Package names in
the table are relative to the module `github.com/tkakkie/ribbitto`.

| Package | Top-level test | Exact message |
|---|---|---|
| cmd/ribbitto | TestFirstUnreadBench | set RIBBITTO_FIRST_UNREAD_BENCH=1 outside CI |
| cmd/ribbitto | TestReadAdvanceCost | set RIBBITTO_READ_COST=1 |
| internal/conversation/conversationpg | TestUnreadBench | set RIBBITTO_UNREAD_BENCH=1 outside CI |
| internal/realtime | TestStreamCost | set RIBBITTO_STREAM_COST=1 to measure the stream's cost |
| internal/unread/internal/postgres | TestReadMergeCost | set RIBBITTO_READ_COST=1 |

The constant allowlist keys all three fields. The test must finish skipped,
be top-level (no slash), have no subtests, and contain the message in its
own output events, possibly inside a file/line diagnostic. Its package must
also have a passing leaf test. Accepted skips never count as passes.
Any other skip, package result `skip`, or package that skips everything
gives `NOT VERIFIED` with names. Failure anywhere wins over skips and gives
`FAILED`; all events and final results are still checked. Ordinary `test`
continues to reject every skip.

Successful host reports start with exactly `verified`, then the passing leaf
count, then (if present) `allowed measurement skips: <package>/<Test>, ...`
in sorted order. With aggregation, scope follows the first line, then the
whole retained record replaces raw test output.

`make check`'s AST scan in `cmd/argonreview/container_allowlist_scan_test.go`
walks repository test files except caches/vendor, finding `.Skip`, `.Skipf`
and `.SkipNow` calls whose literal messages name a measurement gate of the
form `RIBBITTO_<NAME>=1`.
It handles literal concatenation and attributes closures to their enclosing
top-level function; fixture strings are not calls. Database variables and
`RIBBITTO_REQUIRE_BROWSER`, `RIBBITTO_BROWSER_SHARD`,
`RIBBITTO_BROWSER_COUNT_FILE` are excluded explicitly.
The site set must equal the trusted allowlist in both directions: new
gated sites require a tier A allowlist review; stale or renamed entries fail.
Gated measurement skips must use a constant message via `t.Skip` or a verb-free
`t.Skipf` with no further arguments; the scan rejects any `%` in a gated `Skipf`.
The scan cannot see a gate passed through a helper that calls `t.Skip(reason)`.
That skip stays `NOT VERIFIED`: it is not allowlisted.

This convenience for honest code is **not a security control**: Argon can
fabricate events. CI stays the check.

## Result summary

Summary wiring (#1846) is in force since 2026-10-27 (#1700 stage A,
launcher `argonreview-2e3a6bf`); Argon read results inline in preflight.

MCP shares `container-check`'s aggregation record. It gives verdict, then
check/package/scope, test-only package and leaf counts plus allowed skips,
skipped or failing names, test excerpts, compiler/panic/timeout/package
diagnostics, generated totals/paths/gofmt/diff, stderr, and omission counts.
Names cap at 30; excerpts at 8 lines of 200 bytes; diff at 16 lines. Bytes bind
first: at most 2,048 for verified non-errors; 7,000 for errors, including a
512-byte wrapper/error-text reserve. A hard cut is the backstop.
Generated totals precede paths and survive their cap. Unparsed generated diffs
get a trusted diagnostic; omitted excerpts are counted.

[Name masking and sanitisation](argon-verify-log.md#secret-tripwire) apply in every mode.
The decided first line stays, except success with
incomplete facts or a leaf-count/allowed-skip mismatch downgrades MCP alone to
`NOT VERIFIED: summary inconsistent with verdict`.
MCP drops verified build/vet stderr and stderr marked not servable; the host
retains it. Runner errors, timeouts, interruptions (even after non-verified
reports) and cleanup failures get trusted metadata and the fixed minimal body:
“Details unavailable: the check did not complete.” This also applies when
a cap, malformed stop or deadline wins the first line alongside
a real runner, cleanup or interruption failure. An aggregator stop alone keeps
its normal body, including the cap's cut-stream diagnostic; its own
cap/malformed cancellation is not a runner failure. Cleanup failures and late
context errors govern both outputs; incomplete verification never verifies.

On 2026-10-28 the maintainer accepted that the attribution of a client kill
to our own cancellation is best-effort, because a successful process-group
kill cannot tell our kill from an independent SIGKILL of the same client
racing our own cap or malformed stop; in that rare case the result is never
`verified`, it is FAILED when a confirmed failure exists and can be NOT VERIFIED
when the output cap hit without one, its first line follows the
[N1 precedence](argon-verify-aggregation.md#n1-verdict-precedence), and only
the body and the servability of stderr differ.

## Timeout and cancel results

#1857's timeout label is merged but **not in force until the pinned launcher
is updated (#1700)**. Enabling needs separate approval after Claude repeats
host preflight from the trusted merge build. The run limit stays 10 minutes,
including source preparation; MCP's call limit is unchanged. Host and MCP
share the decided line; MCP's wrapper supplies it for empty pre-container reports.

| Situation | First line |
|---|---|
| Run deadline while the container runs | `NOT VERIFIED: time limit exceeded` |
| Deadline after a complete pass, including final processing | `NOT VERIFIED: time limit exceeded` |
| Deadline together with the output cap | `NOT VERIFIED: output limit exceeded (output cut off)` |
| Complete failure, skips or no tests; deadline during stop, removal or source removal | Own failing or `NOT VERIFIED` verdict |
| Deadline before a container, or MCP call deadline | Wrapper timeout line; a call deadline reaching the runner uses the run deadline rule |
| Cleanup timeout alone, runner error or status 125 | Existing `FAILED` |
| Source cleanup failure | `FAILED: source cleanup failed` |
| Parent cancel before a container | Wrapper's `NOT VERIFIED: cancelled` |
| Parent cancel while running, or after a pass | Existing `FAILED` |
| Parent cancel after another verdict | Existing verdict; stop/removal still gives `FAILED` |

Only the run context decides a run deadline, not independent cleanup timeouts.
Parent deadlines reaching it follow that rule; cancellation follows the table.
Unconfirmed removal keeps `FAILED` unless a cap wins
[N1 precedence](argon-verify-aggregation.md#n1-verdict-precedence).
Caps/malformed stops/deadlines with real runner, cleanup or interruption failures
get the minimal MCP body (#1846); stops alone keep normal bodies ([summary](#result-summary)).
