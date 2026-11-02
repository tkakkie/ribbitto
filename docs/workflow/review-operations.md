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
  before asking the maintainer, and `ai-reviewed` requires every required
  review to cover the delivered head.
- **Counts only when it completes:** a report on the PR at its head commit,
  each finding with a disposition (fixed, follow-up issue, or declined with
  reason). A failed, timed-out or empty run never counts: record it on the PR,
  retry once, then ask the maintainer.
- **After a later push** (fix, Copilot follow-up, merge of `main`) whose diff
  touches those areas, review again over the PR's whole diff at the new head.
  An earlier report covers a later head only when every later commit is outside
  those areas. A ready PR goes back to a draft until the review completes. A fix
  for a finding of this review first passes the
  [follow-up check](review-followups.md#copilot-follow-up-check).
- **Running it:** the [README recipe](README.md#roles-and-running-the-ais)
  with `.github/prompts/adversarial.md` and [review context](#running-a-review).
  Post `**Adversarial review — <AI>** (at <SHA>)`. The template's *Adversarial*
  field names the AI and reviewed SHA, `pending`, or `not required` with reason.
- **Who reviews, and the requirement check:** an AI eligible under
  [review independence](README.md#principles), decided from the actual commits;
  check the requirement against what the diff does, not only touched paths.
