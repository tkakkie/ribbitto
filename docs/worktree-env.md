# Worktree environments

`make ai-env` creates/migrates a database hashed from the resolved worktree root through
`RIBBITTO_TEST_DATABASE_URL`. Its ignored `.env.local` stores only its name and
app/metrics ports. `make dev`, `make migrate DIRECTION=up|down|status`,
`make seed ARGS='-messages 100'` and `make schema-docs` read it. For other
commands: `python3 scripts/ai_env.py run <command>`. Without `.env.local`,
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
`make ai-env-clean` drops only this worktree's dev database and releases ports.
`make ai-health` distinguishes missing/invalid admin configuration, connection
failures and SQL failures without credentials;
`make check` runs it before tests when DB configuration is set or required.
`make check-ai-env` tests this tooling.

Run `python3 scripts/ai_capacity.py WT1 WT2 PACKAGES PARALLEL` outside the
sandbox, with this change in both worktrees and `bin/ai-db` built. It reports
two uncached required-DB checks' exit codes and counts of lines matching
`53300|too many clients`, sampled peak/initial client backends (including one
persistent observer connected before checks), server limit, `-p`, `-parallel`
and pool limits. Each check runs in its own process group, killed on cleanup.
Output stays in a 0600 `bin/ai-capacity-*.log` in each worktree; logs may contain
credentials, so do not share them. The report never prints URLs. The runner
reuses the inherited/default module cache and GOPATH. SQL pools are unbounded
(0); pgx pools use pgx's default, max(4, CPUs). The admin URL cannot carry
`pool_max_conns`: pgtest's admin connection is plain pgx, which would send it to
the server as a setting.

Measured on 2026-10-09 (10 CPUs, pgx pools of 10): two uncached required-DB
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

