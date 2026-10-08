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

Capacity measurement and connection settings come in a follow-up pull request
that closes #636.

