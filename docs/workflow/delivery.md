# Delivering a pull request

Read when: orchestrating a PR ([implementer brief](#standard-implementer-brief), reviews, Copilot, third-AI review; rounds/independence in [`reviewing.md`](reviewing.md)) or scanning a PR body before publishing ([Running a review](#running-a-review)).

## Standard implementer brief

Paste into every implementer run (once `ready`), with the issue number, branch,
risk and issue text:

> You are `<Codex|Argon>`, implementing issue `#<N>` in `tkakkie/ribbitto` on
> branch `<ai>/<topic>` from `origin/main` (`ready`, risk: `<normal|high>`).
>
> Issue `#<N>` — `<title>`:
> `<issue body, including Done when>`
>
> 1. Read `AGENTS.md` and only the touched area's `docs/` (`AGENTS.md` "Where
>    things are explained"). Use only your own token; never put a credential in
>    a commit, pull request, comment or log; use only a disposable local test
>    database (`RIBBITTO_TEST_DATABASE_URL`), never a shared one. Skip
>    `docs/workflow/` unless an action triggers a section:
>    own token, credentials, `ready`, merge (docs/workflow/README.md#what-no-ai-does),
>    tool failures, disposable test DBs (docs/workflow/ai-credentials.md#tool-failures-and-disposable-databases),
>    PR body credential scan (docs/workflow/delivery.md#running-a-review), or
>    measurement caps (docs/workflow/investigations.md#investigations-and-the-performance-target).
> 2. Implement the issue in this worktree (diff under ~400 lines excluding
>    generated files). Stop and report if a maintainer decision or a new
>    language/tool is needed. For a behaviour-preserving conversion of a script to a module, before opening the PR run a test or script that exercises a copy of the old script (taken from the baseline SHA, since the conversion deletes it) and the new module on the same inputs and compares outputs — clock, storage failures, malformed values included — (`node --test` in `web/test` for DOM-free logic, the existing `TestBrowser` suites (or a new case in them) as the comparison when real browser behaviour is needed); the PR body names the baseline SHA and records the commands and the result (kept as evidence there, not as a permanent file); a restructuring with no old/new pair records what was compared instead ([#2](https://github.com/tkakkie/ribbitto/issues/2#issuecomment-6559340753)).
> 3. Run `make check` and `make prepush` (plus `make deps` if imports change);
>    fix failures without deleting, skipping or weakening a test.
> 4. Commit (Conventional Commits, `Co-Authored-By:` trailer), push only your
>    branch, run the credential scan (docs/workflow/delivery.md#running-a-review)
>    on the PR body file, and open a DRAFT PR (`.github/pull_request_template.md`,
>    `Closes #<N>`).
> 5. Wait for CI (`gh pr checks`), fix failures (`make prepush` before any
>    follow-up push), and stop once CI is green. Do not set labels, request or
>    run reviews, mark ready, merge, or touch `main`, other branches, settings
>    or tokens.

## Waking the orchestrator

Run every implementer as one blocking background command (no `nohup` or trailing `&`), so its end/time limit wakes the orchestrator (#1457). On wakeup, read the result: with a PR, continue through parallel reviews, fixes and follow-up checks to `ai-reviewed` until a rule here, in [reviewing.md](reviewing.md) or [README](README.md) says to ask the maintainer. If it stopped for a maintainer decision, report at once; if it failed or timed out, inspect worktree/branch and resume/restart once, else report. CI and Copilot wait loops cap at 60 minutes (at the cap, report to the maintainer and do not proceed). The maintainer decides `ready`, merges, measurement approval and the cases where the rules say to ask.

## Running a review

- Run via the [README recipe](README.md#roles-and-running-the-ais) in a clean worktree at the reviewed head (after `git fetch`). Pass issue/PR number, exact base/head (or baseline) SHAs, round and earlier findings; the reviewer reads the rest with `gh` (own token) and `git diff` (the adversarial reviewer reads the context in `.github/prompts/adversarial.md`, not other reports/comments).
- A background run counts as started only once its log header shows expected `workdir:` and `model:`; an earlier exit is a failed start (#359).
- Never pass the Status issue (#1) or have a reviewer fetch it.
- Post with `gh issue comment` or `gh pr comment`; never rely on `@codex review` (checks only the most severe problems).
- Report a tool that will not run; never re-sign, patch or replace it.
- Before publishing a PR body (`gh pr create --body-file body.md`), run this scan (joins `\`-continued lines and flags URLs with user info, #358); it prints nothing, or review each match (redact real credentials; harmless placeholders/prose may stay):

  ```sh
  sed -e :a -e '/\\$/{' -e '$!N' -e '}' -e 's/\\\n//' -e ta body.md |
    grep -niE '://[^/@[:space:]]*@|password|passwd|secret|token|(api|access|private)[-_[:space:]]?key'
  ```

- CodeQL (`.github/workflows/codeql.yml`) runs on `main` pushes and weekly, not on PRs; read `main` alerts in the Security tab.
- Keep scratch files outside the worktree or delete them before checks such as `go test ./...`; git-ignored paths (`bin/`) are no exception (builds/tests use them).

## Requesting Copilot

Copilot (advisory only) reviews each PR **once** (unless [docs-only](#docs-only-pull-requests)) at **Lite** (`.github/instructions/code-review.instructions.md`); Balanced is not used (UI-only in *Reviewers*, not CLI/API). Request it on the draft (`gh pr edit <n> --add-reviewer @copilot`, #681) in parallel with other reviews on the same head with current `main` (`main` ruleset does not request it on ready; drafts/pushes do not trigger it); repeat only if the maintainer asks. Wait (up to the [60-minute cap](#waking-the-orchestrator); at the cap, report to the maintainer and do not proceed) until its review of that head is submitted (body-only reviews count: read them), then answer/resolve its comments; any change uses the follow-up check below as a draft.

## Copilot follow-up check

PRs only, after a Copilot or [third-AI](#third-ai-review) (adversarial) fix (not a review round; two-round limit untouched). If answering needs a change:

1. Apply only changes answering this round's findings (Copilot and parallel-review fixes in follow-up commits), each named. Each third-AI fix includes a regression test failing without it; only if no automated test can reproduce it (rule/doc fix, or a race no existing harness can gate; never effort/time) record why and alternative verification (scripted/manual reproduction, mutation showing the guard matters, or exact text change). Keep the PR draft (re-draft first if the maintainer asked for another Copilot review after ready).
2. An AI that did not write the change ([review independence](README.md#principles)) checks via `.github/prompts/copilot-check.md` ([README recipe](README.md#roles-and-running-the-ais), [review context](#running-a-review)) that the whole diff since the baseline SHA (the round's reviewed head) contains only those changes, each answers its finding, and each third-AI fix's test fails without it (running it or reading a pre-fix CI run) — or the no-test reason holds and alternative verification was done (unaccepted reason: ❌). Header: `Copilot` only if a Copilot finding is covered, else `Adversarial`; retry non-counting runs once per README; record who ran it:

   ```
   **<Copilot|Adversarial> follow-up check — <Claude|Codex|Argon>** (at <SHA>)
   Compared: <baseline SHA>..<SHA>
   - ✅ | ❌ finding — how checked (third-AI: fix + test failing without it, or no-test reason + alternative verification)
   ```

3. All pass → run [third-AI review](#third-ai-review) on the whole PR diff at the new head when required and no report covers that head, or repeat it under the [later-push rule](#third-ai-review) if an earlier report exists (a push before it restarts this check), then complete the lifecycle. Do not re-request Copilot unless the maintainer asks (if it reviews again anyway, handle once as above, then ask the maintainer). Any other diff change, design/spec decision, failed check or new blocking finding → ask the maintainer.

## Docs-only pull requests

Copilot is skipped when all four hold (#1234):
- documentation only: Markdown files and `docs/size-exceptions.txt`;
- no gate document (`docs/workflow/**`, `.github/**`, `AGENTS.md`, `CLAUDE.md`, `docs/domain/invariants.md`);
- risk `normal` ([Risk](reviewing.md#risk));
- no loosened check (loosening `docs/size-exceptions.txt` is not exempt).

All other lifecycle steps and [checks](../../AGENTS.md#commands) stay; note the docs-only rule in the PR body, handle unsolicited Copilot reviews as above, and restore the normal chain (with Copilot) if a condition stops holding.

## Third-AI review

Required for PRs the README lists (`high` risk, or touching concurrency, caches/background work, real-time delivery, ordered transactions, authentication, authorisation, organisation scoping, or removing/skipping/loosening a check), and changes to rules guarding those areas or AI/CI tooling: `.github/prompts/**`, `.github/instructions/**`, `.github/workflows/**`, review-gate docs (`docs/workflow/**`, `.github/pull_request_template.md`, `CLAUDE.md`) and security/scoping rules in `AGENTS.md` and `docs/domain/invariants.md` (docs not exempt). When in doubt, review; past clean reviews never excuse skipping one. Findings are advisory, except blocking [conversion parity](reviewing.md#pull-request-cross-review).

- **When:** in parallel with cross-review and Copilot on the same head SHA on a draft PR with current `main`. A substantive push (not a typo or meaning-preserving rewording) makes older reports non-final; applied fixes are checked before asking the maintainer, and `ai-reviewed` needs every required review to cover the delivered head.
- **Counts only when complete:** a report on the PR names its head SHA, each finding with a disposition (fixed, follow-up issue, or declined with reason). Failed/timed-out/empty runs never count: record on the PR, retry once, then ask the maintainer.
- **After a later push** (fix, Copilot follow-up, merge of `main`) whose diff touches those areas, re-review the whole PR diff at the new head (an earlier report covers a later head only if later commits are outside them; a ready PR returns to draft until complete). Finding fixes here first pass the [follow-up check](#copilot-follow-up-check).
- **Running it:** [README recipe](README.md#roles-and-running-the-ais) with `.github/prompts/adversarial.md` and [review context](#running-a-review); post `**Adversarial review — <AI>** (at <SHA>)`. The template's *Adversarial* field names the AI and reviewed SHA, `pending`, or `not required` with reason.
- **Who reviews / requirement check:** an AI eligible under [review independence](README.md#principles) from actual commits; check the requirement by what the diff does, not only touched paths.
