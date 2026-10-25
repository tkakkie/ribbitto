# Verify database check

## Status

`test-db` (#1697 PR B1) is merged but **not in force until #1700**.
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
Browser tests, cgo-dependent packages and race checks remain CI-only.

In this pull request every skip still gives `NOT VERIFIED`, including all
five measurement skips. The trusted measurement-skip allowlist and its scan
follow in the next pull request of #1697. Until then, `cmd/ribbitto`,
`internal/realtime`, `internal/unread/internal/postgres` and conversationpg
report `NOT VERIFIED` when their measurement tests skip.

`test` and `test-db` use the same verdict reader: failure anywhere gives
`FAILED`; any skip gives `NOT VERIFIED` with names. Successful reports
start with exactly `verified`, then `N passing leaf tests`. The summary
precedes captured output. An empty stream gives `FAILED`, even on exit 0.

CI stays the check; code can fabricate test events, as it can today.
