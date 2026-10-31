# Worktree environments

`make ai-env` creates/migrates a database hashed from the resolved worktree root through
`RIBBITTO_TEST_DATABASE_URL`. Its ignored `.env.local` stores only its name and
app/metrics ports. `make dev`, `make migrate DIRECTION=up|down|status`,
`make seed ARGS='-messages 100'` and `make schema-docs` read it. For other
commands: `./bin/ai-db run <command>` (`cmd/devdb`). Without `.env.local`,
shell configuration applies. Local names and integer ports must match the registry
before any connection; only the parsed admin URL's database field changes.

Automatic ports use 20000–32767, below the usual OS ephemeral ranges,
starting at a path-derived candidate; `ai-env.json` in the shared Git
directory is published atomically under the OS advisory lock `ai-env.lock`.
Allocation skips reserved/bound IPv4/IPv6 ports; `AI_APP_PORT`/`AI_METRICS_PORT`
overrides use the same checks. Reruns keep free reservations and replace busy ones.
Entries use stable Git worktree IDs (or `main`); resolved paths are retargeted on
moves, retaining the original database name. Subdirectories use the Git root.
Relative Git backlinks resolve from their metadata directory. Linked entries also
record a random token stored there in `ribbitto-ai-env`; moves, copies and Git repair
preserve it. A missing or different token marks a reused Git ID, releasing its old
ports and dropping its old database. The main worktree needs no token.
IDs absent from Git's worktree list are cleaned up too; prunable entries remain
until pruned. No prefix scans or test-template cleanup.
`make ai-env-clean` drops this worktree's dev database, removes its `.env.local`
and releases its ports, and, like every `make ai-env` run, also drops the
databases and releases the ports of stale registry entries.
`make ai-health` distinguishes missing/invalid admin configuration, connection
failures and SQL failures without credentials;
`make check` runs it before tests when DB configuration is set or required.
`cmd/devdb` (`bin/ai-db reserve`, `release`, `run`, `health`) implements all of
this; its Go tests cover each rule.

Build with `go build -o bin/ai-capacity ./cmd/capacity`, then run
`bin/ai-capacity WT1 WT2 PACKAGES PARALLEL` outside the sandbox, with this
change in both worktrees and `bin/ai-db` built. Run the built binary so targeted
signals reach its cleanup handler. It reports
two uncached required-DB checks' exit codes and counts of lines matching
`53300|too many clients`, sampled peak/initial client backends (including one
persistent observer connected before checks), server limit, `-p`, `-parallel`
and pool limits. Each check runs in its own session. Cleanup kills its process
group and rescans for descendants, including those that leave the group; zombies are ignored.
A failed `ps` scan fails the run because cleanup cannot be confirmed. SIGINT,
SIGTERM and SIGHUP also clean up. A descendant that calls `setsid()` leaves
the session and cannot be found (accepted limit, #636).
Output stays in a 0600 `bin/ai-capacity-*.log` in each worktree; logs may contain
credentials, so do not share them. The report never prints URLs. The runner
reuses the inherited/default module cache and GOPATH. SQL pools are unbounded
(0); the report reads the fixed pool limit from `pgtest.MaxConns` (#849). Other
pgx pools use pgx's default, max(4, CPUs). The admin URL cannot carry
`pool_max_conns`: pgtest's admin connection is plain pgx, which would send it to
the server as a setting.

Measured on 2026-10-09, before #849 (10 CPUs, pgx pools of 10): two uncached required-DB
`make check` runs at `-p 2 -parallel 10` both passed, peaking at 49 client
backends of `max_connections` 100, with no 53300. A second run at that setting
confirmed it (peak 38/100, no 53300). The shared server therefore keeps
`max_connections=100` (`compose.yml`), and:

- `make check` runs `go test` at `-p 2` by default (`GO_TEST_FLAGS ?= -p 2` in
  the `Makefile`). `go test`'s own default, `-p` = CPUs, can exceed the limit.
  The capacity runner and explicit measurements override it with
  `GO_TEST_FLAGS`.
- Run at most two full `make check` at once.
- To raise `-p` to 4 or more, first re-measure at that setting, then change
  `max_connections` and the default together.

CI runs the rest shard in its own `test-rest` job beside `checks`.
Each test job has its own PostgreSQL service. `tools/testshard` owns
packages, browser selectors and Go test flags; `-run` passes these directly to
`make check-test`. Browser slices cover every selector of one total and run
only `TestBrowser`; one non-browser slice runs `-skip ^TestBrowser$`.
`-check` verifies the matrix, exact runner commands and local Makefile wiring,
rejecting independent package lists or flags. `-run` sets `GOFLAGS=-count=1`
for every shard, whatever the workflow sets, because setup-go restores Go's test result cache; the
workflow's `GOFLAGS` line is redundant. Web runs as `web-nonbrowser`, `web-1/3`,
`web-2/3` and `web-3/3`; `cmd` runs `cmd/ribbitto` as a whole package.
Local `make check-test` runs every package with the same default timeout;
CI's package coverage and browser selection remain checked by `-check`.
Selectors use `RIBBITTO_BROWSER_SHARD=k/n`; empty or unset runs all scenarios,
and malformed or empty shards fail.
Registration filters full scenario names: the six slowest are pinned in
serpentine order, and the rest use FNV-1a modulo n. The real-name coverage test
and `-check` share `internal/web/testdata/browser-shard-totals.txt`.
`-check` also checks the browser-only test's declaration in source; a compile-time
reference pins its name in the web tests. The runner sets
`RIBBITTO_BROWSER_COUNT_FILE` to a temporary file, then prints its count after
`make check-test`, even on failure. No count is printed if browser setup fails.
