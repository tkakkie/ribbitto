# Adversarial review

**A pull request gets the adversarial review its tier asks for before the
maintainer is asked**, whatever its risk class: Grok (tier A), Muse Code or
Antigravity (tier B), or none (tier C). The required review must run; its
findings are advisory. It does not count towards the two review rounds. It
runs after the cross-review, while the pull request is still a draft, and
before it is marked ready for review.

## Which review runs

The tier follows what the change alters, **wherever the code lives**
(handlers, use cases, stores, `db/queries/**`, `db/migrations/**`, scripts),
not its risk class, which only decides how much the maintainer reads. Grok's
findings have come from concurrency, long-lived state and the gates
themselves (#339): of 74 Grok reviews in the 100 pull requests up to #497,
the 8 with findings were #334, #427 and #431 (real time and retention),
#336 and #347 (launchers), and #338, #340 and #364 (gates), while step 3's
about 30 org, identity and web refactors had none (#498).

**Precedence: A, then B, then C.** A change that touches an A area is tier A
even as a move, an extraction, a new seam or tests only (#427 moved retention
into realtime's store). **When in doubt, the higher tier.**

- **A — Grok required:**
  - concurrency, caches or background work (goroutines, locks, TTLs,
    clean-up and retention jobs);
  - real-time delivery, on the server (`internal/realtime/**`, the stream
    handlers and renderers in `internal/web`, the event log's queries and
    readers) or in the browser (the SSE scripts in `web/static`);
  - transactions whose order matters across writers: the organisation's
    `event_seq` and the realtime append in posting, branching, setup and
    sign-up;
  - the rules that guard these areas and the AI and CI tooling: the AI
    launchers in `scripts/ai/**`; the trusted prompts and review
    instructions in `.github/prompts/**` and `.github/instructions/**`;
    `.github/workflows/**`; the files that define the review gates
    (`docs/workflow/README.md`, `reviewing.md`, `running-other-ai.md`, this
    whole file, and `.github/pull_request_template.md`); and the security
    and scoping rules in `AGENTS.md` and `docs/domain/invariants.md`. Being
    documentation does not exempt a change from this item.
- **B — one review by Muse Code, or by Antigravity when the maintainer runs
  it:**
  - a change that **alters behaviour** in authentication, authorisation,
    sessions or organisation scoping: who is signed in, who is a member,
    what a member may see or do, and how a session expires (for example in
    `internal/org/**`, `internal/identity/**`, `internal/web/middleware/**`,
    the setup, sign-up and sign-in handlers, and their stores and queries);
  - a change to `Makefile`, `tools/**` or a linter, vet or code-generation
    setting (`.golangci.yml`, `sqlc.yaml`) that removes, skips or loosens a
    check `make check` or CI runs.
- **C — none:** everything that matches no A or B area: behaviour-preserving
  changes in B's areas (moves, renames, extractions, consumer-declared seams
  and their closures), tests only outside A's areas, documentation outside
  the gate documents, lint rules that only tighten, dependency bumps, and
  ordinary feature work outside A and B.

The template's *Adversarial* field names the review: `Grok`,
`Muse Code (B)`, `Antigravity (B)`,
`Muse Code (Grok unavailable until <date>)`, or
`skipped (C: <reason>)`, for example "behaviour unchanged" or "outside A and
B: <area>". The other AI checks the tier against what the diff does, not
only the paths it touches; a change that alters behaviour in a B area is not
C.

**A required review counts only when it completes:** a report on the pull
request at its head commit, each finding with a disposition. A time-out, an
outage or an empty run is not a review: retry once, then ask the maintainer.
When Grok is unavailable (quota or outage), a tier A pull request gets Muse
Code under the same rule, and the maintainer decides whether to merge or
wait for Grok.

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

## Muse Code

Muse Code (#330, from the trial in #246) is tier B's reviewer and tier A's
fallback when Grok is unavailable, under the completion rule above. It may
also review any other pull request as an optional, extra adversarial
reviewer; an extra run never satisfies or waives a requirement (the
required tier's review, the Claude ↔ Codex rounds, Copilot's follow-up, the
maintainer's decisions).

- It reviews the same head commit with the same context Grok gets, run as in
  [*Running Muse Code*](running-other-ai.md#running-muse-code).
- Claude posts the report as `**Adversarial review — Muse Code** (at <SHA>)`.
  A valid finding gets a disposition like any other: fixed, a follow-up
  issue, or declined with the reason.
- If an **extra** run fails (a provider outage, the time limit), record that
  in the pull request and carry on. A failed **required** run is retried
  once, then the maintainer decides.
