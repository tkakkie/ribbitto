Read when: orchestrators launch reviews, request Copilot, or check the docs-only and third-AI requirements.

# Review operations

## Running a review

- Run via the [README recipe](README.md#roles-and-running-the-ais) in a clean worktree at the reviewed head (after `git fetch`). Pass issue/PR number, exact base/head (or baseline) SHAs, round and earlier findings; the reviewer reads the rest with `gh` (own token) and `git diff` (the adversarial reviewer reads the context in `.github/prompts/adversarial.md`, not other reports/comments).
- A background run counts as started only once its log header shows expected `workdir:` and `model:`; an earlier exit is a failed start (#359).
- Never pass the Status issue (#1) or have a reviewer fetch it.
- Post with `gh issue comment`/`gh pr comment`; never rely on `@codex review` (checks only the most severe problems).
- Report a tool that will not run; never re-sign, patch or replace it.
- CodeQL (`.github/workflows/codeql.yml`) runs on `main` pushes and weekly, not on PRs; read `main` alerts in the Security tab.
- Keep scratch files outside the worktree or delete them before checks such as `go test ./...`; git-ignored paths (`bin/`) are no exception (builds/tests use them).

## Requesting Copilot

Copilot (advisory only) reviews each PR **once** (unless [docs-only](#docs-only-pull-requests)) at **Lite** (`.github/instructions/code-review.instructions.md`); Balanced is not used (UI-only in *Reviewers*, not CLI/API). Request it on the draft (`gh pr edit <n> --add-reviewer @copilot`, #681) in parallel with other reviews on the same head with current `main` (`main` ruleset does not request it on ready; drafts/pushes do not trigger it); repeat only if the maintainer asks. Wait (up to the [60-minute cap](delivery.md#waking-the-orchestrator); at the cap, report to the maintainer and do not proceed) until it submits its review of that head (body-only reviews count: read them), then answer/resolve its comments; any change uses the [follow-up check](review-followups.md#copilot-follow-up-check) as a draft.

## Docs-only pull requests

Copilot is skipped when all four hold (#1234):
- documentation only: Markdown files and `docs/size-exceptions.txt`;
- no gate document (`docs/workflow/**`, `.github/**`, `AGENTS.md`, `CLAUDE.md`, `docs/domain/invariants.md`);
- risk `normal` ([Risk](reviewing.md#risk));
- no loosened check (loosening `docs/size-exceptions.txt` is not exempt).

Other lifecycle steps and [checks](../../AGENTS.md#commands) stay; note the docs-only rule in the PR body, handle unsolicited Copilot reviews as in the [follow-up check](review-followups.md#copilot-follow-up-check), and restore the normal chain (with Copilot) if a condition stops holding.

## Third-AI review

Required for PRs the README lists (`high` risk, or touching concurrency, caches/background work, real-time delivery, ordered transactions, authentication, authorisation, organisation scoping, or removing/skipping/loosening a check), and changes to rules guarding those areas or AI/CI tooling: `.github/prompts/**`, `.github/instructions/**`, `.github/workflows/**`, review-gate docs (`docs/workflow/**`, `.github/pull_request_template.md`, `CLAUDE.md`) and security/scoping rules in `AGENTS.md` and `docs/domain/invariants.md` (docs not exempt). When in doubt, review; past clean reviews never excuse skipping one. Findings are advisory, except blocking [conversion parity](reviewing.md#pull-request-cross-review).

- **When:** in parallel with cross-review and Copilot on the same head SHA on a draft PR with current `main`. A substantive push (not a typo or meaning-preserving rewording) makes older reports non-final; applied fixes are checked before asking the maintainer, and `ai-reviewed` needs every required review to cover the delivered head.
- **Counts only when complete:** a report on the PR at its head commit, each finding with a disposition (fixed, follow-up issue or declined with reason). Failed/timed-out/empty runs never count: record on the PR, retry once, then ask the maintainer.
- **After a later push** (fix, Copilot follow-up, merge of `main`) whose diff touches those areas, re-review the whole PR diff at the new head (an earlier report covers a later head only if every later commit is outside them; a ready PR returns to draft until complete). Finding fixes here first pass the [follow-up check](review-followups.md#copilot-follow-up-check).
- **Running it:** [README recipe](README.md#roles-and-running-the-ais) with `.github/prompts/adversarial.md` and [review context](#running-a-review); post `**Adversarial review — <AI>** (at <SHA>)`. The template's *Adversarial* field names the AI and reviewed SHA, `pending` or `not required` with reason.
- **Who reviews / requirement check:** an AI eligible under [review independence](README.md#principles) from actual commits; check the requirement by what the diff does, not only touched paths.
