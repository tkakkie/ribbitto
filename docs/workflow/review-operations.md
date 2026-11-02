Read when: orchestrators launch reviews, request Copilot, or check the docs-only and third-AI requirements.

# Review operations

## Running a review

- Run through the [README recipe](README.md#roles-and-running-the-ais) in a
  clean worktree at the reviewed head (after `git fetch`). Pass the issue or PR
  number, exact base and head (or baseline) SHAs, round and earlier findings;
  the reviewer reads the rest with `gh` (own token) and `git diff` (the
  adversarial reviewer reads the context in `.github/prompts/adversarial.md`,
  not other reports or comments).
- A background run counts as started only after its log header shows the
  expected `workdir:` and `model:`; an earlier exit is a failed start (#359).
- Never pass the Status issue (#1) or have a reviewer fetch it.
- Post with `gh issue comment` or `gh pr comment`; do not rely on `@codex review`
  (it checks only the most severe problems).
- Report a tool that will not run; never re-sign, patch or replace it.
- CodeQL (`.github/workflows/codeql.yml`) runs on pushes to `main` and weekly,
  not on PRs; read its alerts on `main` in the Security tab.
- Scratch files go outside the worktree, or are deleted before checks such as
  `go test ./...`; git-ignored paths (`bin/`) are no exception because builds
  and tests use them.

## Requesting Copilot

Copilot reviews each PR **once** (unless [docs-only](#docs-only-pull-requests)),
at **Lite** (`.github/instructions/code-review.instructions.md`), as an
advisory reviewer only. Request it on the draft (`gh pr edit <n> --add-reviewer
@copilot`, #681) in parallel with the other reviews on the same head once the
branch has current `main`. The `main` ruleset does not request it on ready, and
drafts and pushes do not trigger it. Repeat only if the maintainer asks.
Balanced is not used (UI-only in *Reviewers*, not CLI/API).

Wait (up to the [60-minute cap](delivery.md#waking-the-orchestrator); at the cap, report
to the maintainer and do not proceed) until its review of that head is
submitted (a body-only review counts: read it), then answer and resolve its
comments. Any change goes through the
[follow-up check](review-followups.md#copilot-follow-up-check) as a draft.

## Docs-only pull requests

Copilot is skipped when all four hold (#1234):

- documentation only: Markdown files and `docs/size-exceptions.txt`;
- no gate document: nothing in `docs/workflow/**`, `.github/**`,
  `AGENTS.md`, `CLAUDE.md` or `docs/domain/invariants.md`;
- risk `normal` ([Risk](reviewing.md#risk));
- no loosened check (a `docs/size-exceptions.txt` change that loosens one is not exempt).

All other lifecycle steps and [checks](../../AGENTS.md#commands) stay; note the
docs-only rule in the PR body. Handle any unsolicited Copilot review as in the
[follow-up check](review-followups.md#copilot-follow-up-check).
If a condition stops holding, restore the normal chain, Copilot included.

## Third-AI review

Required for the PRs the README lists (`high` risk, or touching concurrency,
caches or background work, real-time delivery, ordered transactions,
authentication, authorisation, organisation scoping, or a check removed,
skipped or loosened), and for changes to the rules guarding those areas or to
AI and CI tooling: `.github/prompts/**`, `.github/instructions/**`,
`.github/workflows/**`, the review-gate documents (`docs/workflow/**`,
`.github/pull_request_template.md`, `CLAUDE.md`) and the security and scoping
rules in `AGENTS.md` and `docs/domain/invariants.md`; documentation is not
exempt. When in doubt, review; a past clean review never excuses skipping one.
Findings are advisory, except blocking [conversion parity](reviewing.md#pull-request-cross-review).

- **When:** in parallel with cross-review and Copilot on the same head SHA
  while the PR is a draft with current `main`. Each report names its SHA; a
  substantive push (not a typo or meaning-preserving rewording) makes older
  reports non-final. Each finding gets a disposition, applied fixes are checked
  before asking the maintainer, and `ai-reviewed` is set only when every
  required review and the follow-up checks cover the delivered head.
- **Cap and what counts:** at most **two completed runs** per pull request
  ([#2](https://github.com/tkakkie/ribbitto/issues/2#issuecomment-6578659085)).
  A run counts only when it completes: a report on the PR at its head commit,
  each finding with a disposition (fixed, follow-up issue, declined with
  reason, or — for a blocking conversion-parity finding after the cap —
  accepted by the maintainer under
  [Pull request cross-review](reviewing.md#pull-request-cross-review)). A
  failed, timed-out or empty run never counts: record it on the PR, retry once,
  then ask the maintainer. Follow-up checks
  ([`review-followups.md`](review-followups.md)) are not runs. The cross-review
  limit (two rounds, then closure verification or the maintainer) is unchanged.
- **Second run (after a later push before the cap):** after a later push (a fix
  for the first run, a Copilot follow-up, or a merge of `main`) whose diff
  touches those areas, a ready PR goes back to a draft and a fix for a finding
  of this review first passes the
  [follow-up check](review-followups.md#copilot-follow-up-check); then run the
  second review. The second run reports `Compared: <first run's SHA>..<head>`
  and reviews that whole delta: the fixes of the first run, any Copilot fixes
  and merges of `main` made in between, and their effect on unchanged code that
  calls into it (a second run that reads only the fix commits is not enough).
  `.github/prompts/adversarial.md` carries the same scope. An earlier report
  covers a later head without another run only when every later commit is
  outside those areas (or, at the cap, is covered by the post-cap rules below).
- **After the second run (at the cap):** no third adversarial run. When
  findings remain after the second run, follow the
  [post-cap triage](reviewing.md#pull-request-cross-review); the orchestrator
  does not start another fix round on its own. A fix the maintainer chooses
  after the cap, a Copilot fix or a merge of `main` is checked by an
  independent [follow-up check](review-followups.md#copilot-follow-up-check) of
  exactly that delta **only when it is limited**: it changes no behaviour,
  security boundary or interaction with existing code beyond answering the
  named findings (an independent checker may classify a clean merge of `main`
  as limited only after establishing that it leaves the pull request's
  behaviour, security boundaries and interactions unchanged, including effects
  through callers, direct or transitive dependencies and shared contracts;
  uncertainty goes to the maintainer). A change that substantively alters
  behaviour, a security boundary or the interaction with existing code (for
  example a merge of `main` that changes code the diff depends on, or an
  additional fix that goes beyond the named findings) is **never approved by a
  limited follow-up check alone**, and this rule takes precedence even when the
  change solely answers a named finding or implements a maintainer-selected
  fix: the orchestrator escalates to the maintainer, who may require it to go
  into a separate pull request that gets the normal review (cross-review and
  third-AI review from the start). On escalation the orchestrator withholds
  `ai-reviewed` and records the maintainer's explicit chat decision, the
  applicable SHA and the required verification before proceeding, preserving
  all existing gates; the re-confirmation of an accepted difference cannot
  approve the substantive delta or automatically restart a fix/review cycle. If
  a fix itself needs a fix, the orchestrator asks the maintainer instead of
  starting another round. If the head changes after the maintainer accepts a
  conversion-parity difference
  ([Pull request cross-review](reviewing.md#pull-request-cross-review)), an
  independent [follow-up check](review-followups.md#copilot-follow-up-check)
  confirms at the final SHA that the accepted occurrence conditions and the
  effect are unchanged (the same scenario, the same effect, no new path to it);
  if anything differs, the maintainer decides again and `ai-reviewed` waits.
- **Running it:** the [README recipe](README.md#roles-and-running-the-ais)
  with `.github/prompts/adversarial.md` and [review context](#running-a-review).
  Post `**Adversarial review — <AI>, run <1|2>** (at <SHA>)` (with
  `Compared: <first run's SHA>..<head>` on run 2). The template's *Adversarial*
  field names the AI, the completed run count (`1` or `2` of `2`), the reviewed
  SHAs (and any follow-up check SHA covering the delivered head), `pending`, or
  `not required` with reason; its *Accepted parity differences* field records
  any maintainer-accepted conversion-parity differences or `n/a`.
- **Who reviews, and the requirement check:** an AI eligible under
  [review independence](README.md#principles), decided from the actual commits;
  check the requirement against what the diff does, not only touched paths.
