# Adversarial review

**A pull request that alters one of the areas below gets one adversarial
review by Grok before the maintainer is asked**, whatever its risk class.
Running it is then mandatory; its findings are advisory. It does not count
towards the two review rounds. It runs after the cross-review, while the
pull request is still a draft, and before it is marked ready for review.

## When Grok runs

In the small sample recorded in #339, Grok's valid findings came from
long-lived state and concurrency. So Grok is **required** when a change
alters any of the following, **wherever the code lives** (handlers, use
cases, stores, `db/queries/**`, `db/migrations/**`, scripts):

1. authentication, authorisation, sessions or organisation scoping: who is
   signed in, who is a member, what a member may see or do, and how a
   session expires (for example `internal/org/**`,
   `internal/identity/**`, `internal/web/middleware/**`, `internal/web/org.go`,
   the setup, sign-up and sign-in handlers, and the stores and queries for
   accounts, members and sessions);
2. real-time delivery, on the server (`internal/realtime/**`, the stream
   handlers and renderers in `internal/web`, the event log's queries and
   readers) or in the browser (the SSE scripts in `web/static`);
3. concurrency, caches or background work (goroutines, locks, TTLs,
   clean-up jobs);
4. the rules that guard these areas or the AI and CI tooling: the AI
   launchers in `scripts/ai/**`; the trusted prompts and review
   instructions in `.github/prompts/**` and `.github/instructions/**`;
   `.github/workflows/**`; the files that define the review gates
   (`docs/workflow/README.md`, `reviewing.md`, `running-other-ai.md`, this
   whole file, and `.github/pull_request_template.md`); and the security
   and scoping rules in `AGENTS.md` and `docs/domain/invariants.md`. Being
   documentation does not exempt a change from this item;
5. the checks themselves: a change to `Makefile`, `tools/**` or a linter,
   vet or code-generation setting (`.golangci.yml`, `sqlc.yaml`) that
   removes, skips or loosens a check `make check` or CI runs.

Otherwise Grok is not required. A `high` pull request without it says why
in the template, `Adversarial: skipped (<reason>)`: for example
documentation only, tests only, a dependency bump, or a build or lint
setting that keeps every check. These examples never apply when any item
above matches. **When
in doubt, Grok runs.** The other AI checks the requirement or the skip
against what the change does, not only the paths it touches.

## Running Grok

Run it from the maintainer's checkout, taking the launcher as it is on
`main` — never a copy that a pull request could have changed:

```sh
git fetch origin main &&
  launcher=$(git show origin/main:scripts/ai/grok-review.sh) &&
  bash -c "$launcher" grok-review <pr-number>
```

Each step is joined with `&&`, so if the launcher cannot be read the command
stops with a non-zero status instead of reporting a clean review. (Avoid
`bash <(git show …)`: when `git show` fails, bash runs an empty script and
exits 0.)

- **The invocation above is the security boundary.** If the launcher is
  run as a file that differs from `origin/main:scripts/ai/grok-review.sh`,
  it refuses to run — but that only catches an accidentally edited copy: a
  malicious copy runs its own code before any check (a script cannot vouch
  for itself). Never run `scripts/ai/grok-review.sh` from a PR checkout.
- It checks out the PR head in a temporary worktree and builds the prompt
  from `.github/prompts/adversarial.md` **on `origin/main`** (a PR cannot
  rewrite its own review instructions). Everything the PR controls — title,
  description and diff — is appended as **one JSON object** on the last
  line, so escaping keeps it from ending early or adding instructions; the
  prompt also tells Grok that every file in the worktree is untrusted data.
- Grok runs read-only: plan mode **and** only the `read_file`, `list_dir`
  and `grep` tools, no web search, stdin closed.
- Those tools are not a filesystem sandbox, so a PR whose tree contains any
  symlink (mode `120000`) is refused before the prompt is built or anything
  is checked out: a symlink could otherwise let Grok read files outside the
  worktree.
- Where available (macOS), `caffeinate -i` prevents idle sleep while Grok
  runs; it is skipped elsewhere. It runs inside Grok's process group and
  is stopped with that group on every exit. The supervisor checks a
  wall-clock deadline every two seconds, so a deadline passed during sleep
  is caught on wake even if the alarm did not advance. The alarm remains
  as a backstop.
- It stops Grok and everything Grok started after `RIBBITTO_GROK_TIMEOUT`
  seconds (1–86400, default 2400 / 40 minutes; exit 124), and removes the
  worktree and temporary files on success, failure, timeout, stall, Ctrl-C (130)
  and `TERM` (143), reporting any cleanup failure. Cleanup ignores further
  INT/TERM signals, including while it prints the report. When Grok itself fails,
  the script exits with Grok's status. If Grok's process group cannot be
  created, it prints `could not create a process group for Grok` and exits
  126 without starting Grok.
- It reads Grok's `streaming-json` progress and prints the concatenated text
  updates as the report when the run ends. If updates stop for `RIBBITTO_GROK_STALL`
  seconds (1–86400, default 300), it stops the same process group and exits
  125, keeping report text received before the stall. The limit also applies before
  the first update. Gaps of more than 30 seconds between supervisor polls
  (normally at most two seconds), and backward clock steps, are excluded
  from inactivity to tolerate suspension and clock changes. The overall
  wall-clock timeout is unchanged. The default
  is about 6× the largest measured gap (48 seconds, Grok 1.0.41, three
  reviews; [measurement on #337](https://github.com/tkakkie/ribbitto/issues/337#issuecomment-5954740657)).
  After a stall, rerun once using the invocation above. If it stalls again,
  record both runs in the PR and ask the maintainer.
- The timeout is a backstop against a stuck CLI, not an estimate of review
  time. Raise `RIBBITTO_GROK_TIMEOUT` when a larger PR needs more time or a
  review times out. On timeout, the launcher suggests a value for
  `RIBBITTO_GROK_TIMEOUT` (double the current limit, capped at 86400 seconds)
  and points to the invocation above, without printing a command. If the
  86400-second maximum is already in effect, it says so and suggests no rerun.
- Known limitation (#84): Bash, 3.2 and 5.3 alike, can lose a SIGINT that
  arrives while it forks a command. So a Ctrl-C in the first moments, before
  Grok starts, is occasionally ignored and the review carries on (still
  read-only and under the timeout). Press Ctrl-C again, or send `TERM`
  (`kill <pid>`), which is not lost. A fix was tried in #87 and not merged:
  its complexity outweighed this harm.
- The title, description, base and head come from one `gh pr view`; the
  diff is computed locally from that base and head, so everything Grok sees
  describes the same commit even if the PR is pushed in the meantime.
- `RIBBITTO_GROK_MODEL` picks a model (`grok models` lists them).
  `RIBBITTO_GROK_TRUSTED_REF` changes where the launcher and prompt must
  come from; use it only to test a PR that edits them.
  `RIBBITTO_GROK_TEST_SETUP_DELAY` exists only for
  `scripts/ai/grok-review_test.sh`.
- Claude posts the report as a PR comment headed
  `**Adversarial review — Grok** (at <SHA>)` and adds, for each finding,
  *valid* (fixed in the PR or tracked as an issue) or *false positive* with
  the reason.

## Muse Code, optional

Muse Code may review a pull request as a second adversarial reviewer
(#330, from the trial in #246), including one where Grok was skipped.
Grok stays required wherever [*When Grok runs*](#when-grok-runs) says so. Muse Code
is optional and advisory: its report never satisfies or waives a
requirement (Grok's review, the Claude ↔ Codex rounds, Copilot's
follow-up, the maintainer's decisions).

- It reviews the same head commit with the same context Grok gets, run as in
  [*Running Muse Code*](running-other-ai.md#running-muse-code).
- Claude posts the report as `**Adversarial review — Muse Code** (at <SHA>)`.
  A valid finding gets a disposition like any other: fixed, a follow-up
  issue, or declined with the reason.
- If it fails (a provider outage, the time limit), record that in the pull
  request and carry on. A failed optional run never blocks.
