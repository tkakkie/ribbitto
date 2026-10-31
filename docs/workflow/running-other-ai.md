# Running the other AI

## Headless reviews

Headless CLIs may await input forever. **Always close stdin and set a
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

Post: `gh issue comment` / `gh pr comment`. Do not rely on
GitHub's `@codex review`: it looks only at the most severe problems.

Argon reviews use the [README recipe](README.md#roles-and-running-the-ais)
and the kept prompts, with the PR or issue, relevant commits and earlier
findings supplied in the prompt text. The same rights and review
independence rules apply to every AI.

## Handoff of another AI's code

For code Codex or Argon wrote (maintainer, 2026-10-16,
[#1220](https://github.com/tkakkie/ribbitto/issues/1220)): no unreasoned
difference between the AIs, enough safety for practice, little to operate.

- **In its own sandbox,** the implementing AI may build and test as before:
  see the [README](README.md#what-every-ai-may-do).
- **After handoff, no AI runs another AI's unverified code directly on
  the host**, outside those sandboxes: not `make check`, `make prepush`,
  `go test`, a build, `make dev` or any tool from that tree. Nor transferred
  `node_modules`, npm scripts or Node tests.
- The only standing exception is an [approved performance measurement](investigations.md#host-measurements).
- **CI is the check.** GitHub Actions runs PR code with read-only access
  and no secrets. Green draft CI replaces `make check` before the
  first review; green CI on each follow-up head replaces `make prepush`;
  closure verification and follow-up checks read CI without re-running code.
- **Still done on the host:** code and diff review, the credential grep on
  `PR_BODY.md` ([below][c]).
- **UI changes:** CI browser tests default; maintainer decides whether/how essential manual checks run case by case.

Handoff builds need no Node. Once #1411/#1820 land, Go asset generation must
precede compilation in the isolated [build routes](investigations.md#build-routes).

A branch with only Claude's own code on top of `main` is not a handoff:
Claude follows the [local checks](../../AGENTS.md#commands) and
[follow-up push checks](reviewing.md#follow-up-pushes).

## Running Codex

Maintainer setup, not general Codex limits (codex-cli 0.153; evidence: #48).

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
shows the expected `workdir:` and `model:`. An earlier exit is a failed
start (#359).

### Implementation brief

Brief: issue and review comments, size class, starting files/packages,
and the [roles section](README.md#roles-and-running-the-ais).
Starting points; read the change's dependencies.

`small`: expected adversarial tier C and either documentation only or an
expected diff under about 100 lines → `medium`. Otherwise `normal`: current
default, no effort flag. Tier A/B is always `normal`, including gate documents under
`docs/workflow/`.

The brief also includes:

- [Codex Node use](codex-node.md): routine only after #1410/#1415 merge; #1410's one-time exception has ended.
- **Go caches.** Only the worktree is writable, so use
  `GOCACHE=$PWD/bin/.cache/go-build GOMODCACHE=$PWD/bin/.cache/mod
  GOLANGCI_LINT_CACHE=$PWD/bin/.cache/lint GOPATH=$PWD/bin/.cache/gopath`
  (`bin/` is git-ignored).
- **Database.** Use a disposable test database following
  [credentials](ai-credentials.md#tool-failures-and-disposable-databases),
  with `RIBBITTO_TEST_DATABASE_URL` and `RIBBITTO_REQUIRE_DB=1`.
  If the current environment cannot provide a database or a required
  process tool, record the missing checks and use CI for them.
- **Handoff.** Codex does not commit, push or use GitHub. It leaves the
  changes in the working tree and writes a draft PR description to an
  untracked `PR_BODY.md`, which never contains a connection string with
  credentials, a password, a token or a key: a command that needs one shows
  a placeholder such as `<local test database>` (#358). It stays inside
  the handoff on the machine ([#2](https://github.com/tkakkie/ribbitto/issues/2#issuecomment-5891926762)):
  no other AI CLIs (claude, agy), no browser or Computer Use: reviews are the
  orchestrator's, and browser checks follow the
  [handoff][h]. It never re-signs, patches or
  replaces a tool binary to make it run, and reports the failure instead.
  Scratch files go outside the worktree if allowed; this profile forbids it,
  so Codex deletes scratch files before checks such as `go test ./...`
  and before handoff. Git-ignored paths such as `bin/` are no exception:
  builds and tests use them. The orchestrator also reports a tool that
  will not run, never modifying it.

### Handoff checks

The orchestrator, running nothing from the tree
([handoff][h]):

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
   [handoff rule][h].

### Review context

**Reviews.** Supply the issue and, for a PR, the PR with comments, diff
and CI; never supply or fetch Status #1. Review effort follows the tier
above; implementation effort follows size.

[h]: #handoff-of-another-ais-code
[c]: #running-codex
