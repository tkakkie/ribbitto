# Adversarial review

From M1 on, **every `high` pull request gets one adversarial review by Grok
before the maintainer is asked.** Running it is mandatory; its findings are
advisory. It does not count towards the two review rounds. It runs after the
cross-review, while the pull request is still a draft, and before it is
marked ready for review.

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
  worktree and temporary files on success, failure, timeout, Ctrl-C (130)
  and `TERM` (143), reporting any cleanup failure. When Grok itself fails,
  the script exits with Grok's status. If Grok's process group cannot be
  created, it prints `could not create a process group for Grok` and exits
  126 without starting Grok.
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

Muse Code may review a `high` pull request as a second adversarial
reviewer (#330, from the trial in #246). Grok stays mandatory. Muse Code
is optional and advisory: its report never satisfies or waives a
requirement (Grok's review, the Claude ↔ Codex rounds, Copilot's
follow-up, the maintainer's decisions).

- It reviews the same head commit with the same context as Grok, run as in
  [*Running Muse Code*](running-other-ai.md#running-muse-code).
- Claude posts the report as `**Adversarial review — Muse Code** (at <SHA>)`.
  A valid finding gets a disposition like any other: fixed, a follow-up
  issue, or declined with the reason.
- If it fails (a provider outage, the time limit), record that in the pull
  request and carry on. A failed optional run never blocks.
