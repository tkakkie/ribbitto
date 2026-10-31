# Delivering a pull request

What follows the [README lifecycle](README.md#lifecycle) for a pull request:
running the reviews, Copilot, and what is checked before the maintainer
decides. Who reviews and how many rounds are in the README and
[`reviewing.md`](reviewing.md).

## Running a review

- Run it through the [README recipe](README.md#roles-and-running-the-ais) in
  a clean worktree at the head under review (after `git fetch`). The prompt
  names the issue or pull request, the full commits compared and the earlier
  findings; the reviewer reads the rest with `gh` and its own token.
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
the draft (`gh pr edit <n> --add-reviewer @copilot`, verified on #681), after
the other AI approves and the branch has the current `main`. The `main`
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

1. Apply only the changes that answer Copilot; the pull request stays a
   draft. (If the maintainer asks for another Copilot review after the pull
   request was marked ready, convert it back to a draft first.)
2. An AI that did not write the change checks that the whole diff since the
   approved SHA contains only those changes and each is right, using
   `.github/prompts/copilot-check.md` through the README recipe with the pull
   request, the approved and head SHAs, Copilot's findings and the diff. A
   failed or non-counting run is retried once as the README says; record who
   ran it. It reports:

   ```
   **Copilot follow-up check — <Claude|Codex|Argon>** (at <SHA>)
   Compared: <approved SHA>..<SHA>
   - ✅ | ❌ Copilot comment — how it was checked
   ```

3. All pass → the third-AI review where the README requires one, then the
   remaining lifecycle steps. Copilot is not re-requested unless the
   maintainer asks; if it reviews again anyway, handle that once the same
   way, then ask the maintainer. A change that needs a design or
   specification decision, a failed check or a new blocking finding → ask the
   maintainer.

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
concurrency, caches, real-time delivery, ordered transactions,
authentication, authorisation, organisation scoping, or a check that is
removed, skipped or loosened). The same review is required for a change to
the rules that guard those areas and to the AI and CI tooling:
`.github/prompts/**`, `.github/instructions/**`, `.github/workflows/**`, the
review-gate documents (everything under `docs/workflow/`,
`.github/pull_request_template.md`, `CLAUDE.md`) and the security and scoping
rules in `AGENTS.md` and `docs/domain/invariants.md`; being documentation does not exempt a change.
When in doubt, review. A past review without findings is never a reason to
skip one. Its findings are advisory.

- **When:** after the other AI approves and its findings are fixed, the
  branch has the current `main`, and Copilot's review and any follow-up check
  have passed; on that head, while the pull request is a draft, immediately
  before `ai-reviewed`. It does not run alongside the first cross-review.
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
  with `.github/prompts/adversarial.md`, the prompt naming the pull request,
  the commits, the diff and the earlier findings. Post
  `**Adversarial review — <AI>** (at <SHA>)`. The template's *Adversarial*
  field names the AI and the reviewed SHA, `pending`, or `not required` with
  the reason. The other AI checks the requirement against what the diff
  does, not only the paths it touches.
