# Running the other AI

Headless CLIs can wait forever for input. **Always close stdin and set a
time limit.** macOS has no `timeout`, so use Perl's `alarm`:

```sh
limit() { perl -e 'alarm shift; exec @ARGV' "$@"; }   # limit 900 cmd args…
```

Codex reviews Claude's work:

```sh
# Read-only Codex cannot reach GitHub, so the orchestrator fetches the
# context first and passes it on stdin (which also keeps stdin from being
# left open). Every fetch is chained with &&, so if any of them fails, Codex
# does not start on partial context.

# Issue: the issue with its comments (earlier rounds) and the Status issue.
ctx=$(mktemp) &&
  gh issue view N --json title,body,comments > "$ctx" &&
  gh issue view 1 --json body >> "$ctx" &&
  limit 900 codex exec -s read-only -o review.md \
    "Review this GitHub issue following docs/workflow/reviewing.md. Reply in the report format." < "$ctx"

# Pull request (in a clean worktree at the PR head, after git fetch): the
# PR with its comments (earlier rounds), the linked issue and the Status issue.
# Not `codex exec review --base …`: it rejects a custom prompt.
ctx=$(mktemp) &&
  gh pr view P --json title,body,comments > "$ctx" &&
  gh issue view N --json title,body,comments >> "$ctx" &&
  gh issue view 1 --json body >> "$ctx" &&
  limit 1800 codex exec -s read-only -o review.md \
    "Review this pull request: run 'git diff origin/main...HEAD'. Its GitHub context is on stdin. Follow docs/workflow/reviewing.md and reply in the report format." < "$ctx"
```

Claude reviews Codex's work: the orchestrating Claude session reviews
directly, or `limit 1800 claude -p "…" < /dev/null`.

Post the result with `gh issue comment` / `gh pr comment`. Do not rely on
GitHub's `@codex review`: it looks only at the most severe problems, so it
does not replace this review.

## Running Codex

How the maintainer's orchestrating sessions run Codex (codex-cli 0.153).
This records that setup, not general limits of Codex. Evidence: #48.

**Implementation.** The global `~/.codex/config.toml` is shared with other
projects and allows only localhost, so do not edit it. Instead, pass a
per-run permission profile with `-c` and run the command from the issue's
worktree. The profile allows:

- writes only to the worktree;
- network access only to localhost, the Go module proxy and checksum
  database, GitHub and the npm registry.

Any other host is refused ("Network access to "example.com" was blocked:
domain is not on the allowlist"). The command uses a Bash array, so run it
from bash or zsh, with `limit` defined as above:

```bash
perms=(
  -c 'permissions.ribbitto.extends=":workspace"'
  -c 'permissions.ribbitto.network.enabled=true'
  -c 'permissions.ribbitto.network.allow_local_binding=true'
  -c 'permissions.ribbitto.network.domains={"localhost"="allow","127.0.0.1"="allow","proxy.golang.org"="allow","sum.golang.org"="allow","storage.googleapis.com"="allow","github.com"="allow","api.github.com"="allow","codeload.github.com"="allow","objects.githubusercontent.com"="allow","release-assets.githubusercontent.com"="allow","raw.githubusercontent.com"="allow","registry.npmjs.org"="allow"}'
  -c 'default_permissions="ribbitto"'
)
limit 3600 codex exec "${perms[@]}" -o result.md "$(cat prompt.md)" < /dev/null
```

The prompt carries the issue and its review comments, plus these rules:

- **Go caches.** Only the worktree is writable, so use
  `GOCACHE=$PWD/bin/.cache/go-build GOMODCACHE=$PWD/bin/.cache/mod
  GOLANGCI_LINT_CACHE=$PWD/bin/.cache/lint GOPATH=$PWD/bin/.cache/gopath`
  (`bin/` is git-ignored).
- **Database.** Codex cannot use Docker, so the orchestrator starts
  PostgreSQL and waits until it is ready. The prompt then says to run
  `make check` with `RIBBITTO_TEST_DATABASE_URL=postgres://postgres:codex-dev-only@127.0.0.1:55433/postgres?sslmode=disable`
  and `RIBBITTO_REQUIRE_DB=1`. Its Go tests run with the race detector,
  which works in this profile. `make vuln` does not, because
  `vuln.go.dev` is not on the allowlist; CI runs it
  ([Checks](../../README.md#checks)). The last step of `make check`,
  `bash scripts/ai/grok-review_test.sh`, stops at once with a message,
  because the sandbox does not permit `ps` and the script needs it to find
  and check its fake Grok processes
  ([#183](https://github.com/tkakkie/ribbitto/issues/183)). Everything
  before it has run by then, so that failure alone is expected.

  ```sh
  # Reuse the container if it is already running; either way, wait for it.
  if ! docker ps --format '{{.Names}}' | grep -qx ribbitto-codex-pg; then
    docker run -d --name ribbitto-codex-pg -p 127.0.0.1:55433:5432 \
      -e POSTGRES_PASSWORD=codex-dev-only postgres:18
  fi &&
  (
    for i in $(seq 1 30); do
      docker exec ribbitto-codex-pg pg_isready -U postgres >/dev/null 2>&1 && exit 0
      sleep 1
    done
    echo "ribbitto-codex-pg: PostgreSQL not ready after 30 tries" >&2
    exit 1
  )
  ```

  Do not start Codex unless this exits 0. If a stopped container with that
  name exists, `docker run` fails. Remove it with
  `docker rm -f ribbitto-codex-pg`, which is also how to clean up when you
  are done. If port 55433 is taken, pick another and change both commands.
- **Handoff.** Codex does not commit, push or use GitHub. It leaves the
  changes in the working tree and writes a draft PR description to an
  untracked `PR_BODY.md`. It also stays inside the handoff on the machine
  ([#2](https://github.com/tkakkie/ribbitto/issues/2#issuecomment-5891926762)):
  no other AI CLIs (claude, grok), no browser or Computer Use, since reviews
  and browser checks are the orchestrator's. It never re-signs, patches or
  replaces a tool binary to make it run, and reports the failure instead. It
  puts scratch files outside the worktree when the runner allows it; this
  profile does not, so Codex may create them in the worktree but deletes
  them before running checks such as `go test ./...` and before handing off.
  Git-ignored places such as `bin/` are no exception: builds and tests still
  pick files up there. The orchestrator likewise reports a tool that will not
  run instead of modifying it.

The orchestrator then:

1. reviews the diff;
2. runs what Codex could not (for example `make db-up`, a live `make dev`,
   or `bash scripts/ai/grok-review_test.sh` outside the sandbox);
3. commits, pushes and opens the PR.

**Reviews.** In `-s read-only` mode Codex cannot reach GitHub, so pipe in
everything the review needs, as the examples above do:

- the issue, or the pull request and its linked issue, with their comments
  (these include the earlier review rounds);
- the Status issue (#1).
