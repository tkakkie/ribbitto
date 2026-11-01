# Delivering a pull request

Read when: orchestrating a PR ([implementer brief](#standard-implementer-brief), reviews, Copilot, third-AI review; rounds and independence in [`reviewing.md`](reviewing.md)), or scanning a PR body before publishing ([Running a review](#running-a-review)).

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
>    language/tool is needed.
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

Start every implementer run as one blocking background command (no `nohup` or
trailing `&`), so its end or time limit wakes the orchestrator (#1457).
On wakeup, read the result: with a PR, continue through the parallel reviews,
fixes and follow-up checks to `ai-reviewed` until a rule in this file,
[`reviewing.md`](reviewing.md) or the [README](README.md) says to ask the
maintainer. If the implementer stopped for a maintainer decision, report at
once; if the run failed or timed out, inspect the worktree and branch and
resume or restart once, else report. CI and Copilot wait loops cap at 60
minutes; at the cap, report to the maintainer and do not proceed. The
maintainer decides `ready`, the merge, measurement approval and the cases where
the rules say to ask.

## Running a review

- Run through the [README recipe](README.md#roles-and-running-the-ais) in a
  clean worktree at the reviewed head (after `git fetch`). Pass the issue or PR
  number, exact base and head (or baseline) SHAs, round and earlier findings;
  the reviewer reads the rest with `gh` (own token) and `git diff` (the
  adversarial reviewer reads the title and description (and the diff), not
  other reports or comments except the earlier findings the orchestrator names).
- A background run counts as started only after its log header shows the
  expected `workdir:` and `model:`; an earlier exit is a failed start (#359).
- Never pass the Status issue (#1) or have a reviewer fetch it.
- Post with `gh issue comment` or `gh pr comment`; do not rely on `@codex review`
  (it checks only the most severe problems).
- Report a tool that will not run; never re-sign, patch or replace it.
- Before publishing a PR body (`gh pr create --body-file body.md`), run this
  scan; it prints nothing, or each match is reviewed (redact real credentials;
  harmless placeholders or prose may stay). It joins `\`-continued lines and
  flags URLs with user info (#358):

  ```sh
  sed -e :a -e '/\\$/{' -e '$!N' -e '}' -e 's/\\\n//' -e ta body.md |
    grep -niE '://[^/@[:space:]]*@|password|passwd|secret|token|(api|access|private)[-_[:space:]]?key'
  ```

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

Wait (up to the [60-minute cap](#waking-the-orchestrator); at the cap, report
to the maintainer and do not proceed) until its review of that head is
submitted (a body-only review counts: read it), then answer and resolve its
comments. Any change goes through the follow-up check below as a draft.

## Copilot follow-up check

PRs only, after a fix for a Copilot or [third-AI](#third-ai-review)
(adversarial) finding; not a review round, so the two-round limit is untouched.
If answering needs a change:

1. Apply only changes answering findings of this round (Copilot's and parallel
   review findings fixed in the follow-up commits), each named. Each fix for a
   third-AI finding includes a regression test that fails without it; only when
   no automated test can reproduce it (a rule/doc fix, or a race no existing
   harness can gate; never effort or time) record why and the alternative
   verification: a scripted/manual reproduction, a mutation showing the guard
   matters, or the exact text change. Keep the PR a draft (convert back to
   draft first if the maintainer asked for another Copilot review after ready).
2. An AI that did not write the change ([review independence](README.md#principles))
   checks that the whole diff since the baseline SHA (the head the round
   reviewed) contains only those changes, each answers its finding, and for
   each third-AI finding the test fails without it (by running it or reading a
   CI run without the fix) — or that the recorded reason holds and the
   alternative verification was done (an unaccepted reason is ❌) — using
   `.github/prompts/copilot-check.md` via the README recipe with
   [review context](#running-a-review). Header: `Copilot` only when a Copilot
   finding is covered, else `Adversarial`. Retry a non-counting run once per the
   README; record who ran it:

   ```
   **<Copilot|Adversarial> follow-up check — <Claude|Codex|Argon>** (at <SHA>)
   Compared: <baseline SHA>..<SHA>
   - ✅ | ❌ finding — how it was checked (for a third-AI finding: the fix, and the test that fails without it, or no test — the reason and alternative verification)
   ```

3. All pass → run the [third-AI review](#third-ai-review) over the PR's whole
   diff at the new head when the README requires one and no adversarial report
   covers the head, or repeat it under the [later-push rule](#third-ai-review)
   when an earlier report exists (a push before it restarts this check), then
   the remaining lifecycle steps. Do not re-request Copilot unless the
   maintainer asks; if it reviews again anyway, handle once the same way, then
   ask the maintainer. Any change in the diff other than those answering
   findings, design/spec decision, failed check or new blocking finding → ask
   the maintainer.

## Docs-only pull requests

Copilot is skipped when all four hold (#1234):

- documentation only: Markdown files and `docs/size-exceptions.txt`;
- no gate document: nothing in `docs/workflow/**`, `.github/**`,
  `AGENTS.md`, `CLAUDE.md` or `docs/domain/invariants.md`;
- risk `normal` ([Risk](reviewing.md#risk));
- no loosened check (a `docs/size-exceptions.txt` change that loosens one is not exempt).

All other lifecycle steps and [checks](../../AGENTS.md#commands) stay; note the
docs-only rule in the PR body. Handle any unsolicited Copilot review as above.
Stopping meeting a condition restores the normal chain, Copilot included.

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
Findings are advisory.

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
  them. A ready PR goes back to a draft until the review completes. A fix for a
  finding of this review first passes the [follow-up check](#copilot-follow-up-check).
- **Running it:** the [README recipe](README.md#roles-and-running-the-ais)
  with `.github/prompts/adversarial.md` and [review context](#running-a-review).
  Post `**Adversarial review — <AI>** (at <SHA>)`. The template's *Adversarial*
  field names the AI and reviewed SHA, `pending`, or `not required` with reason.
- **Who reviews, and the requirement check:** an AI eligible under
  [review independence](README.md#principles), decided from the actual commits;
  check the requirement against what the diff does, not only touched paths.
