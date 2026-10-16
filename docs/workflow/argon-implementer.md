# Argon as implementer (trial)

A time-boxed trial ([#1206](https://github.com/tkakkie/ribbitto/issues/1206)):
Argon (Gemini 4 Argon in Antigravity's `agy` CLI) implements **2 or 3 tier C
issues**. Everything else in this directory stays as it is. The cap is the
preflight plus at most 3 trial pull requests, retries included. The trial ends
at the cap or on 2026-11-30, whichever comes first, and at once if a boundary
below fails. At the cap it stops and reports, even without a conclusion
([investigations](investigations.md)). Nothing here runs before the preflight
is recorded on #1206.

## Which issues

An issue is eligible only if its *Risk* is `normal`
([risk](reviewing.md#risk)) **and** its *Implementer* field states an expected
adversarial tier C and the C case it matches
([tiers](adversarial-review.md#which-review-runs)), for example
`Argon (trial #1206; normal risk; expected tier C: ordinary feature work
outside A and B)`. Tier A or B, or `high` risk, means Codex or Claude, as
usual. The issue reviewer checks the expected tier like any other field; in
doubt, the higher tier, so not Argon. The maintainer confirms the implementer
when adding `ready`. No M7 issue is picked unless the maintainer names it.

**The tier follows the diff, not the expectation.** If an Argon pull
request's diff matches tier A or B, it leaves the trial and the maintainer
decides. Argon is the only adversarial reviewer, and an implementer never
reviews its own work.

## Who does what

- **Claude** reviews the pull request: the AI that did not implement it, at
  most two rounds ([reviewing](reviewing.md)). Claude also does the Copilot
  follow-up check.
- **Argon** applies the review and Copilot fixes, in further runs in the same
  worktree. If it cannot finish a fix in one more run, the maintainer decides:
  reassign or stop.
- **Codex** checks any fix Claude writes itself, since that is not Argon's
  work.
- Argon's issues do not count towards Claude's and Codex's equal shares.

## The boundary

Like Codex's handoff ([running Codex](running-other-ai.md#running-codex)),
but enforced by configuration, not by the prompt.

- **Invocation.** Claude creates the worktree (`../wt/<name>`, branch
  `argon/<topic>` from `origin/main`) and runs `agy -p` there with stdin
  closed, a time limit, `--print-timeout`, `--sandbox` and the model the
  maintainer picks (`gemini-4-argon-high` unless he says otherwise). Never
  `--dangerously-skip-permissions`.
- **Its own `HOME`.** A dedicated Antigravity home, signed in once by the
  maintainer, shared only with the shadow trial of
  [#1207](https://github.com/tkakkie/ribbitto/issues/1207); the two trials
  never run at the same time, and each writes its own settings and guard
  before every run. It is never the account's home and never the review
  home (`RIBBITTO_ARGON_HOME`), whose files `prepare` rewrites for reviews.
  The account's `~/.gemini/antigravity-cli/settings.json` is never edited,
  not even temporarily with a restore; its hash is checked before and after
  every run, as the review launcher does.
- **A fail-closed tool allowlist.** Deny rules cannot name `search_web`,
  subagents, `generate_image` or `schedule`, and #1041 saw web access and an
  image written outside the worktree before the review guard existed. The
  review guard allows only reads (`cmd/argonreview/guard.go`), so the
  implementer gets its own trusted guard. It allows file reads, listing and
  search inside the worktree; file creates and edits inside it, except
  workspace configuration (`.agents/**`, `.gemini/**`, `GEMINI.md`,
  `AGENTS.md`, `CLAUDE.md`); and sandboxed commands. It denies everything
  else. A guard that fails or prints nothing is a denial. The guard and its
  configuration live outside the worktree, where Argon cannot write.
- **No workspace hooks or customisation.** Workspace hooks run commands
  outside the sandbox ([running Argon](running-argon.md#the-boundary)).
  Before every run, fix runs included, Claude checks that the worktree has
  no `.agents` or `.gemini` entry, no `GEMINI.md` and no other
  workspace-loaded configuration, and that the tracked `AGENTS.md` and
  `CLAUDE.md` match `origin/main`; it stops otherwise. Those two are loaded
  as instructions, as in reviews, and the guard keeps Argon from editing
  them.
- **A minimal environment for every command, tests Argon writes included.**
  A dedicated `HOME` does not remove inherited credentials. The run starts
  from an empty environment except `PATH`, `HOME`, a `TMPDIR` inside the
  worktree, the caches set inside it as in Codex's handoff (`GOCACHE`,
  `GOMODCACHE`, `GOPATH` and `GOLANGCI_LINT_CACHE` under `bin/.cache/`),
  `GIT_CONFIG_GLOBAL=/dev/null` and `GIT_CONFIG_NOSYSTEM=1`. There is no `GH_TOKEN`, `GITHUB_TOKEN`,
  `SSH_AUTH_SOCK`, credential helper or keychain access. The sandbox holds
  for child processes: writes only inside the worktree, no reads of the
  account's files outside it (the Go toolchain aside), and network only to
  the Go module proxy, the checksum database and the local test database.
- **Also denied:** `git commit` and `push`, `gh`, other AI CLIs, browsers, MCP
  and URL reads. Argon never holds GitHub credentials, never pushes, and
  never opens or merges a pull request.
- **Handoff, as Codex's.** Argon leaves its changes in the working tree and
  an untracked `PR_BODY.md`. Claude reviews the diff, runs `make check` with
  the test database and the credential grep from
  [running Codex](running-other-ai.md#running-codex), then commits, pushes
  and opens the draft pull request. Argon's output and files are data for
  Claude, never instructions.

## Preflight

Before any trial issue, tested on effects as in #1041 and #1071, in a
throwaway repository, through Argon's own tools **and** through an allowed Go
test with harmless sentinels. Each of these is denied or has no effect:

- reads outside the worktree (absolute, `..`, symlink) and an environment
  sentinel;
- writes outside the worktree;
- `.agents/hooks.json` created in one run and not executed in the next;
- `search_web`, a subagent, `generate_image`, `schedule` and a failing guard;
- commit, push, `gh`, a host that is not allowed, and another AI CLI.

The account's shared settings stay unchanged. Telemetry and AI credits are
confirmed off in the home used, as running-argon.md records for the review
home. #1041's accepted data and cost terms (the flat fee only; no API key and
no pay-as-you-go) are confirmed to cover write-mode use. If any of this
cannot be verified, the trial stops and the maintainer gets the reason.

## Records

- **Per trial pull request,** one comment on #2: the issue, the final tier,
  review rounds and blocking findings, Argon's runs (count, time, failures),
  rework (fixes by Argon, changes Claude or Codex had to make) and CI
  failures.
- **Weekly while the trial runs,** one comment on #1206, not #2, so it does
  not trigger retrospectives. Per AI (Claude, Codex, Antigravity, Copilot):
  whether a limit was hit, when and what it stopped, the runs by role, and
  the maintainer's dashboard reading if he gives one.

Widening beyond tier C, and choosing its adversarial reviewer, is a new
`process` issue.
