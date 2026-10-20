# Running the other AI

## Headless reviews

Headless CLIs can wait forever. **Close stdin and set a time limit.**
macOS has no `timeout`; use Perl's `alarm`:

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

Post with `gh issue comment` / `gh pr comment`. Do not rely on GitHub's
`@codex review`: it sees only the most severe problems.

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

For Codex/Argon code (maintainer, 2026-10-16,
[#1220](https://github.com/tkakkie/ribbitto/issues/1220)): no unreasoned
difference between the AIs, practical safety, little to operate.

- **In its own sandbox,** the implementing AI may build and test as before:
  Codex: [profile](#running-codex); Argon only within [boundary](argon-implementer.md#the-boundary).
- **After handoff, no AI runs another AI's unverified code directly on
  the host**, outside those sandboxes: not `make check`, `make prepush`,
  `go test`, a build, `make dev` or any tool from that tree. After handoff,
  nobody runs transferred `node_modules`, npm scripts or Node tests on the host.
- The sole standing exception: an [approved performance measurement](investigations.md#host-measurements).
- **CI is the check.** GitHub Actions runs PR code with read-only access
  and no secrets. Green draft CI replaces `make check` before the
  first review; green CI on each follow-up head replaces `make prepush`;
  closure verification and follow-up checks read CI without re-running code.
- **Still done on the host:** code and diff review, the credential grep on
  `PR_BODY.md` ([below](#running-codex)), and Argon's protected-file and
  workspace checks ([boundary](argon-implementer.md#the-boundary)).
- **UI changes:** CI browser tests default; maintainer decides whether/how essential manual checks run case by case.
- Argon's [container verify](argon-verify.md#status) is pending; Codex has no
  Docker. CI remains the check.

A branch with only Claude's code above `main` is not a handoff: follow
[local checks](../../AGENTS.md#commands) and
[follow-up push checks](reviewing.md#follow-up-pushes).

## Running Codex

Maintainer setup (codex-cli 0.153; #48), not general Codex limits.

### Codex permissions

**Implementation.** Do not edit the shared, localhost-only
`~/.codex/config.toml`. Run from the issue's worktree with this per-run
`-c` profile, allowing:

- writes only to the worktree;
- network access only to localhost, the Go module proxy and checksum
  database, GitHub and the npm registry.

Other hosts are refused. Use bash/zsh and `limit` above:

```bash
perms=(
  -c 'permissions.ribbitto.extends=":workspace"'
  -c 'permissions.ribbitto.network.enabled=true'
  -c 'permissions.ribbitto.network.allow_local_binding=true'
  -c 'permissions.ribbitto.network.domains={"localhost"="allow","127.0.0.1"="allow","proxy.golang.org"="allow","sum.golang.org"="allow","storage.googleapis.com"="allow","github.com"="allow","api.github.com"="allow","codeload.github.com"="allow","objects.githubusercontent.com"="allow","release-assets.githubusercontent.com"="allow","raw.githubusercontent.com"="allow","registry.npmjs.org"="allow"}'
  -c 'default_permissions="ribbitto"'
)
# size=small|normal, as stated in the brief.
case $size in
  small) effort=(-c model_reasoning_effort=medium) ;;
  normal) effort=() ;;
  *) false ;;
esac &&
  limit 3600 codex exec "${perms[@]}" "${effort[@]}" -o result.md "$(cat prompt.md)" < /dev/null
```

### Start confirmation

**Started.** Report a background run as running only after its log header
shows expected `workdir:` and `model:`. Earlier exit means failed start (#359).

### Implementation brief

The brief includes the issue, review comments, size class, starting files/packages
and the [Codex implementer entry](README.md#implementer-codex). Read dependencies too.

`small`: expected adversarial tier C and either documentation only or an
expected diff under about 100 lines → `medium`. Otherwise `normal`: current
default, no effort flag. Tier A/B is always `normal`, including gate documents under
`docs/workflow/`.

Include:

- **Node:** [decision 43](../architecture/web-toolchain.md), implemented later
  by #1410/#1415. Until #1410 lands: no routine Node toolchain. Then the
  sandbox permits pinned Node for type checking/tests/dependency management
  and `npm ci --ignore-scripts` from the reviewed lockfile. Trusted setup
  supplies Node; no download/network expansion.
- **Go caches.** Only the worktree is writable, so use
  `GOCACHE=$PWD/bin/.cache/go-build GOMODCACHE=$PWD/bin/.cache/mod
  GOLANGCI_LINT_CACHE=$PWD/bin/.cache/lint GOPATH=$PWD/bin/.cache/gopath`
  (`bin/` is git-ignored).
- **Database.** Codex cannot use Docker. The orchestrator starts and waits
  for PostgreSQL, then the brief says to run
  `make check` with `RIBBITTO_TEST_DATABASE_URL=postgres://postgres:codex-dev-only@127.0.0.1:55433/postgres?sslmode=disable`
  and `RIBBITTO_REQUIRE_DB=1`. The race detector works; CI runs `make vuln`, which needs the disallowed `vuln.go.dev`
  ([Checks](../../README.md#checks)). Without `ps`,
  `check-test` fails in `cmd/argonreview`, `cmd/capacity` and `cmd/dev`
  process tests; `make check` stops before `check-tools`
  ([#183](https://github.com/tkakkie/ribbitto/issues/183)). That failure is
  expected; CI runs the full check ([handoff](#handoff-of-another-ais-code)).

  ```sh
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

  Start only on exit 0. A stopped named container fails `docker run`; remove
  with `docker rm -f ribbitto-codex-pg`,
  also for cleanup. If port 55433 is taken, change both commands.
- **Handoff.** Codex does not commit, push or use GitHub. It leaves
  working-tree changes and untracked `PR_BODY.md` with a draft PR description,
  never a connection string with credentials, a password, a token or a key: a command that needs one shows
  a placeholder such as `<local test database>` (#358). It also stays
  inside the handoff on the machine ([#2](https://github.com/tkakkie/ribbitto/issues/2#issuecomment-5891926762)):
  no other AI CLIs (claude, agy), browser or Computer Use: reviews are the
  orchestrator's; browser checks follow
  [handoff](#handoff-of-another-ais-code). It never re-signs, patches or
  replaces a tool binary to make it run, and reports the failure instead. It
  uses scratch files outside the worktree if allowed; this profile permits
  them only inside, so delete them before checks such as `go test ./...`
  and handoff.
  Git-ignored places such as `bin/` are no exception: builds and tests still
  pick files up there. The orchestrator also reports tool failures; it never modifies tools.

### Handoff checks

The orchestrator, running nothing from the tree
([handoff](#handoff-of-another-ais-code)):

1. reviews the diff;
2. checks the PR text before it is published (#358): require no matches, or review each match, redact real credentials; harmless
   mentions (placeholders, words in prose) may stay. It joins
   backslash-continued lines and flags any URL with user information:

   ```sh
   sed -e :a -e '/\\$/{' -e '$!N' -e '}' -e 's/\\\n//' -e ta PR_BODY.md |
     grep -niE '://[^/@[:space:]]*@|password|passwd|secret|token|(api|access|private)[-_[:space:]]?key'
   ```

3. commits, pushes and opens the draft PR under the
   [handoff rule](#handoff-of-another-ais-code).

### Review context

**Reviews.** Supply the issue and, for a PR, the PR with comments, diff
and CI; never supply or fetch Status #1. Review effort follows the tier above;
implementation effort follows size.
