# Running the other AI

## Headless reviews

Headless CLIs can wait forever for input. **Always close stdin and set a
time limit.** macOS has no `timeout`, so use Perl's `alarm`:

```sh
limit() { perl -e 'alarm shift; exec @ARGV' "$@"; }   # limit 900 cmd args…
```

Codex reviews Claude's work (issues: [Argon first](reviewing.md)):

```sh
# Read-only Codex cannot reach GitHub: pipe its context in.
# Chain fetches with && so Codex never starts on partial context.

# Issue with comments (including earlier rounds).
ctx=$(mktemp) &&
  gh issue view N --json title,body,comments > "$ctx" &&
  limit 900 codex exec -s read-only -c model_reasoning_effort=medium -o review.md \
    "Review this GitHub issue following docs/workflow/reviewing.md. Reply in the report format." < "$ctx"

# PR: clean worktree at its head, after git fetch.
# Not `codex exec review --base …`: it rejects a custom prompt.
# tier=A|B|C; round 2 also sets round1=<SHA>.
case $tier in A|B) effort=high ;; C) effort=medium ;; *) false ;; esac &&
  compared=${round1:+$round1..HEAD} && compared=${compared:-origin/main...HEAD} &&
  ctx=$(mktemp) &&
  printf 'Compared: %s\n' "$compared" > "$ctx" &&
  gh pr view P --json title,body,comments,headRefOid >> "$ctx" &&
  gh issue view N --json title,body,comments >> "$ctx" &&
  gh pr checks P >> "$ctx" &&
  limit 1800 codex exec -s read-only -c model_reasoning_effort="$effort" -o review.md \
    "Review this pull request: run 'git diff $compared'. Context is on stdin. Follow docs/workflow/reviewing.md and reply in the report format." < "$ctx"
```

Claude reviews Codex's work directly, or with
`limit 1800 claude -p "…" < /dev/null`.

Post the result with `gh issue comment` / `gh pr comment`. Do not rely on
GitHub's `@codex review`: it looks only at the most severe problems.

On an Argon pull request, the pull request example above does not
apply: Codex runs in a clean worktree of `origin/main`, never at the pull
request's head, and gets the diff on stdin
([who does what](argon-implementer.md#who-does-what); for a follow-up
check also [more context](argon-pull-requests.md#who-reviews)):

```sh
# $W: Argon's worktree; $clean: clean origin/main; $base: merge base with
# origin/main for round 1, round-1 SHA for round 2, approved SHA (Copilot)
# or reviewed SHA (adversarial) for a follow-up.
# gh pr checks fails unless CI is green, which the review needs first.
# tier=B|C
g() { env -i PATH=/usr/bin:/bin HOME=/nonexistent GIT_CONFIG_GLOBAL=/dev/null \
  GIT_CONFIG_NOSYSTEM=1 git -C "$W" -c core.hooksPath=/dev/null \
  -c core.fsmonitor=false "$@"; }
case $tier in B) effort=high ;; C) effort=medium ;; *) false ;; esac &&
  diff=$(mktemp) && head=$(g rev-parse HEAD) &&
  test "$head" = "$(gh pr view P --json headRefOid -q .headRefOid)" &&
  echo "Compared: $base..$head" > "$diff" &&
  g diff --no-ext-diff --no-textconv "$base..$head" >> "$diff" &&
  gh pr view P --json title,body,comments,headRefOid >> "$diff" &&
  gh pr checks P >> "$diff" &&
  gh issue view N --json title,body,comments >> "$diff" &&
  (cd "$clean" && limit 1800 codex exec -s read-only -c model_reasoning_effort="$effort" -o review.md \
    "Review the pull request diff and GitHub context on stdin. Do not run git or any build here. Follow docs/workflow/reviewing.md and reply in the report format." < "$diff")
```

Argon's setup: [boundary and preflight](argon-implementer.md#argon-as-implementer);
review differences: [Argon's pull requests](argon-pull-requests.md#who-reviews).

## Handoff of another AI's code

One rule for code Codex or Argon wrote (maintainer, 2026-10-16,
[#1220](https://github.com/tkakkie/ribbitto/issues/1220)): no unreasoned
difference between the AIs, enough safety for practice, little to operate.

- **In its own sandbox,** the implementing AI may build and test as before:
  Codex in its profile ([below](#running-codex)), Argon only as its
  [boundary](argon-implementer.md#the-boundary) allows.
- **After handoff, no AI runs another AI's unverified code directly on
  the host**, outside those sandboxes: not `make check`, `make prepush`,
  `go test`, a build, `make dev` or any tool from that tree.
- **CI is the check.** GitHub Actions runs pull request code with read-only
  access and no secrets. Green CI on the draft replaces the local
  `make check` before the first review; after a follow-up push, green CI
  on the pushed head replaces `make prepush`; closure verification and follow-up checks read CI
  instead of re-running anything.
- **Still done on the host:** code and diff review, the credential grep on
  `PR_BODY.md` ([below](#running-codex)), and Argon's protected-file and
  workspace checks ([boundary](argon-implementer.md#the-boundary)).
- **UI changes:** CI's browser tests by default. Where a manual check is
  essential, the maintainer decides case by case whether and how it runs.
- **No Docker verification environment** for now; reconsider when one is
  actually needed.

A branch with only Claude's own code on top of `main` is not a handoff:
Claude follows the [local checks](../../AGENTS.md#commands) and
[follow-up push checks](reviewing.md#follow-up-pushes).

## Running Codex

How the maintainer's orchestrating sessions run Codex (codex-cli 0.153).
This records that setup, not general limits of Codex. Evidence: #48.

### Codex permissions

**Implementation.** The global `~/.codex/config.toml` is shared with other
projects and allows only localhost, so do not edit it. Instead, pass a
per-run permission profile with `-c` and run the command from the issue's
worktree. The profile allows:

- writes only to the worktree;
- network access only to localhost, the Go module proxy and checksum
  database, GitHub and the npm registry.

Any other host is refused ("Network access to "example.com" was blocked:
domain is not on the allowlist"). It uses a Bash array, so run it from bash
or zsh with `limit` as above:

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

### Start confirmation

**Started.** A background Codex run counts as started only once its log
shows the header's `workdir:` and `model:` lines, naming the expected
worktree and model; only then is it reported as running. If the run exits
before those lines appear, such as Codex refusing an untrusted directory,
report it as a failed start (#359).

### Implementation brief

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
  ([Checks](../../README.md#checks)). The sandbox does not permit `ps`, so
  `check-test` fails in the process tests of `cmd/argonreview`,
  `cmd/capacity` and `cmd/dev`, and `make check` stops before `check-tools`
  ([#183](https://github.com/tkakkie/ribbitto/issues/183)). That failure is
  expected; CI runs the full check ([handoff](#handoff-of-another-ais-code)).

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
  untracked `PR_BODY.md`, which never contains a connection string with
  credentials, a password, a token or a key: a command that needs one shows
  a placeholder such as `<local test database>` (#358). It also stays
  inside the handoff on the machine ([#2](https://github.com/tkakkie/ribbitto/issues/2#issuecomment-5891926762)):
  no other AI CLIs (claude, agy), no browser or Computer Use: reviews are the
  orchestrator's, and browser checks follow the
  [handoff](#handoff-of-another-ais-code). It never re-signs, patches or
  replaces a tool binary to make it run, and reports the failure instead. It
  puts scratch files outside the worktree when the runner allows it; this
  profile does not, so Codex may create them in the worktree but deletes
  them before running checks such as `go test ./...` and before handing off.
  Git-ignored places such as `bin/` are no exception: builds and tests still
  pick files up there. The orchestrator likewise reports a tool that will not
  run instead of modifying it.

### Handoff checks

The orchestrator then, running nothing from the tree
([handoff](#handoff-of-another-ais-code)):

1. reviews the diff;
2. checks the PR text before it is published (#358): this prints nothing,
   or each match is reviewed, real credentials are redacted and harmless
   mentions (a placeholder, the word in prose) may stay. It joins
   backslash-continued lines and flags any URL with user information:

   ```sh
   sed -e :a -e '/\\$/{' -e '$!N' -e '}' -e 's/\\\n//' -e ta PR_BODY.md |
     grep -niE '://[^/@[:space:]]*@|password|passwd|secret|token|(api|access|private)[-_[:space:]]?key'
   ```

3. commits, pushes and opens the draft PR under the
   [handoff rule](#handoff-of-another-ais-code).

### Review context

**Reviews.** Supply the issue and, for a PR, the PR with comments, diff
and CI; never supply or fetch Status #1. Effort follows the tier, as in
the snippets above; implementation keeps its setting.
