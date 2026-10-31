# Delivering a pull request

What follows the [README lifecycle](README.md#lifecycle) for a pull request:
running the implementer and the reviews, Copilot, and what is checked before
the maintainer decides. Who reviews and how many rounds are in the README and
[`reviewing.md`](reviewing.md).

## Waking the orchestrator

The orchestrator starts every implementer run as one blocking command in the
background (no `nohup`, no trailing `&`), so the end of the run, including the
time limit, wakes it (#1457). On every end it reads the result: with a pull
request it continues through the parallel reviews, fixes and follow-up checks
to `ai-reviewed` without asking; when the implementer stopped for a maintainer
decision, or the run failed or timed out (retried once as the
[README](README.md#reviews-issues-and-pull-requests) says), it reports to the
maintainer at once. Waiting loops (such as for CI or Copilot) have a maximum
and report when it is reached. The maintainer is asked only for a decision, a
measurement approval and the merge.

## Running a review

- Run it through the [README recipe](README.md#roles-and-running-the-ais) in
  a clean worktree at the head under review (after `git fetch`). The
  orchestrator passes the issue or pull request number, the exact base and head
  (or baseline) SHAs, the review round and the earlier findings; the reviewer
  reads the rest with `gh` (using its own token) and `git diff`.
- A background run counts as started only after its log header shows the
  expected `workdir:` and `model:`; an earlier exit is a failed start (#359).
- Never give a reviewer the Status issue (#1), and never have it fetch it.
- Post the report with `gh issue comment` or `gh pr comment`. Do not rely on
  GitHub's `@codex review`: it looks only at the most severe problems.
- A tool that will not run is reported, never re-signed, patched or
  replaced to make it run.
- Before a pull request body is published, run this on the file passed to
  `gh pr create --body-file` (here `body.md`); it prints nothing, or each
  match is reviewed: real credentials are redacted, harmless mentions (a
  placeholder, the word in prose) may stay. It joins backslash-continued
  lines and flags any URL with user information (#358):

  ```sh
  sed -e :a -e '/\\$/{' -e '$!N' -e '}' -e 's/\\\n//' -e ta body.md |
    grep -niE '://[^/@[:space:]]*@|password|passwd|secret|token|(api|access|private)[-_[:space:]]?key'
  ```

- CodeQL runs from `.github/workflows/codeql.yml` on pushes to `main` and
  weekly, not on pull requests; read its alerts on `main` in the
  repository's Security tab.

- Scratch files go outside the worktree, or are deleted before checks such as
  `go test ./...`; git-ignored paths such as `bin/` are no exception, because
  builds and tests use them.

## Requesting Copilot

Copilot reviews each pull request **once** (not a
[docs-only](#docs-only-pull-requests) one), at **Lite**, guided by
`.github/instructions/code-review.instructions.md`. It is used only for this
review, as an advisory reviewer. The orchestrating AI requests it by hand on
the draft (`gh pr edit <n> --add-reviewer @copilot`, verified on #681), in
parallel with the other reviews, on the same head, once the branch has the
current `main`. The `main`
ruleset does not request it on ready, and drafts and new pushes do not
trigger it. A repeat review is requested only when the maintainer asks.
Balanced is not used: it can only be chosen by hand in the *Reviewers* panel,
and the CLI and API cannot set the effort.

Wait until its review of that head is submitted (a review that only has a
body counts: read it), then answer and resolve its comments like any other.
A change goes through the follow-up check below, still as a draft.

## Copilot follow-up check

Pull requests only; after Copilot's one review; not a review round. A change
made to answer Copilot would otherwise reach the maintainer unreviewed. If
answering needs a change:

1. Apply only the changes that answer findings of this round (Copilot's, and
   those of the parallel reviews fixed in the follow-up commits), each finding
   named; a fix for a third-AI finding keeps its regression test or the
   recorded reason and alternative verification in
   [`reviewing.md`](reviewing.md#adversarial-follow-up-check). The pull request
   stays a draft. (If the maintainer asks for another Copilot review after the pull
   request was marked ready, convert it back to a draft first.)
2. An AI that did not write the change (following
   [review independence](README.md#principles)) checks that the whole diff
   since the baseline SHA (the head the round reviewed) contains only those
   changes, each is right, and any third-AI fix has a regression test that
   fails without it (or the recorded reason and alternative verification hold),
   using `.github/prompts/copilot-check.md` through the README recipe with the
   [review context](#running-a-review). A
   failed or non-counting run is retried once as the README says; record who
   ran it. It reports:

   ```
   **Copilot follow-up check — <Claude|Codex|Argon>** (at <SHA>)
   Compared: <baseline SHA>..<SHA>
   - ✅ | ❌ finding — how it was checked (for a third-AI finding: the fix, and the test that fails without it, or no test — the reason and alternative verification)
   ```

3. All pass → repeat the [third-AI review](#third-ai-review) over the pull
   request's whole diff at the new head only if the commits since the last
   adversarially reviewed head touch its areas, then the remaining lifecycle
   steps. Copilot is not re-requested unless the maintainer asks; if it reviews
   again anyway, handle that once the same way, then ask the maintainer. Any
   other change in the diff, a change that needs a design or specification
   decision, a failed check or a new blocking finding → ask the maintainer.

## Docs-only pull requests

Copilot is not requested for a pull request meeting all four conditions
(#1234):

- documentation only: Markdown files and `docs/size-exceptions.txt`;
- no gate document: nothing in `docs/workflow/**`, `.github/**`,
  `AGENTS.md`, `CLAUDE.md` or `docs/domain/invariants.md`;
- risk `normal` ([Risk](reviewing.md#risk));
- nothing that loosens a check, so a `docs/size-exceptions.txt` change that
  does is not exempt.

Everything else in the lifecycle and the [checks](../../AGENTS.md#commands)
stays. The pull request body states that the docs-only rule applied. If
Copilot reviews anyway, its comments are handled as above. A pull request
that stops meeting a condition returns to the normal chain from that point,
Copilot included.

## Third-AI review

The README says which pull requests get one (`high` risk, or touching
concurrency, caches or background work, real-time delivery, ordered
transactions, authentication, authorisation, organisation scoping, or a check that is
removed, skipped or loosened). The same review is required for a change to
the rules that guard those areas and to the AI and CI tooling:
`.github/prompts/**`, `.github/instructions/**`, `.github/workflows/**`, the
review-gate documents (everything under `docs/workflow/`,
`.github/pull_request_template.md`, `CLAUDE.md`) and the security and scoping
rules in `AGENTS.md` and `docs/domain/invariants.md`; being documentation does not exempt a change.
When in doubt, review. A past review without findings is never a reason to
skip one. Its findings are advisory.

- **When:** in parallel with the cross-review and Copilot, on the same head
  SHA, while the pull request is a draft and has the current `main`. Each
  report names the SHA it reviewed. A substantive push during the review (a
  typo or meaning-preserving rewording is not one) makes older reports
  non-final for the new head. Each finding gets a disposition and applied
  fixes are checked before the maintainer is asked, and `ai-reviewed` is set only when every
  required review covers the head to be delivered.
- **Counts only when it completes:** a report on the pull request at its
  head commit, each finding with a disposition (fixed, a follow-up issue, or
  declined with the reason). A failed, timed-out or empty run never counts:
  record it on the pull request, retry once, then the maintainer decides.
- **After a later push** (a fix, a Copilot follow-up, a merge of `main`)
  whose diff touches those areas, review again over the pull request's whole
  diff at the new head. An earlier report covers a later head only when
  every later commit is outside them. A pull request already marked ready
  goes back to a draft until the review completes.
  A fix for a finding of this review first passes the follow-up check in
  [reviewing](reviewing.md#adversarial-follow-up-check), or Copilot's in
  [Copilot follow-up check](#copilot-follow-up-check), before the re-review.
- **Running it:** the [README recipe](README.md#roles-and-running-the-ais)
  with `.github/prompts/adversarial.md` and the
  [review context](#running-a-review). Post
  `**Adversarial review — <AI>** (at <SHA>)`. The template's *Adversarial*
  field names the AI and the reviewed SHA, `pending`, or `not required` with
  the reason.
- **Who reviews, and the requirement check:** the third-AI reviewer is an AI
  eligible under [review independence](README.md#principles), decided from
  the pull request's actual commits. It checks the requirement against what
  the diff does, not only the paths it touches.
