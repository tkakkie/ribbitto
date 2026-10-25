# Verify database check

## Status

`test-db` (#1697) is merged but **not in force until #1700**.
Production still runs `argonreview-7f607ca` and the old image.
The real container path (Docker, PostgreSQL in the image and the script)
is untested by Codex, which has no Docker. #1700 must validate that path
before enabling it. CI stays the check; this does not replace review or
[handoff checks](running-other-ai.md#handoff-of-another-ais-code).

## Fixed runner

The [runner](../../cmd/argonreview/container_testdb.go) uses the same local
package-pattern validation as `test`; callers cannot supply flags, an
image, environment, command or server settings. The pattern is positional:
`sh -c <fixed script> sh <pattern>`. The shared schema still has exactly
`check` and `package`, with no additional properties.

The POSIX sh script uses `set -eu` and:

1. Copies `/work/.` to `/tmp/work`, including dotfiles, and runs the shared
   templ, sqlc and Tailwind commands there. Generator logs go to stderr.
2. Runs `/usr/lib/postgresql/18/bin/initdb` with
   `-D /home/check/pgdata -U postgres --auth=trust --no-sync`. The data lives
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

All existing limits stay: 2 GiB memory with equal swap limit, 256 PIDs,
2 CPUs, 10 minutes, 4 MiB combined output, and 1 GiB each for the two tmpfs
mounts. Whether `./...` fits with a database is **not established**.
A limit hit gives `FAILED` or `NOT VERIFIED`, never `verified`.
PostgreSQL's own log lines, including ERROR and STATEMENT lines, go to
standard error and count toward the shared 4 MiB output cap. #1700 must
measure this on `./...`; exceeding the cap gives `NOT VERIFIED`, never `verified`.
Browser tests, cgo-dependent packages and race checks remain CI-only.

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

Successful reports start with exactly `verified`, then the passing leaf
count, then (if present) `allowed measurement skips: <package>/<Test>, ...`
in sorted order. The summary precedes captured output.

The AST scan in `cmd/argonreview/container_allowlist_scan_test.go` runs in
`make check`. It walks every repository test file, excluding caches and
vendor directories, and finds `.Skip`, `.Skipf` and `.SkipNow` calls whose
literal messages name a measurement gate of the form `RIBBITTO_<NAME>=1`.
It handles literal concatenation and attributes closures to their enclosing
top-level function; fixture strings are not calls. Database variables and
`RIBBITTO_REQUIRE_BROWSER`, `RIBBITTO_BROWSER_SHARD`,
`RIBBITTO_BROWSER_COUNT_FILE` are excluded explicitly.
The site set must equal the trusted allowlist in both directions: new
gated sites require a tier A allowlist review; stale or renamed entries fail.

This is a convenience for honest code, **not a security control**: code
Argon writes can fabricate any events, as it can today. CI stays the check.
