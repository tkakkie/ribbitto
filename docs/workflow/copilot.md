# Copilot

What follows Copilot's one review of a pull request, and when Copilot is
not requested. The other review rules are in [`reviewing.md`](reviewing.md).

## Requesting Copilot

Copilot reviews each pull request **once** (not a
[docs-only](#docs-only-pull-requests) one), at **Lite**, guided by
`.github/instructions/code-review.instructions.md`. The orchestrating AI
requests that review by hand on the draft
(`gh pr edit <n> --add-reviewer @copilot`, verified on #681), at the point in
the [delivery sequence](pull-requests.md#lifecycle-of-a-pull-request).
The `main` ruleset does not
request it on ready, and drafts and new pushes do not trigger it. A repeat
review is requested only when the maintainer asks. Advisory. Balanced is not
used: it can only be chosen by hand in the *Reviewers* panel, and the CLI and
API cannot set the effort.

Wait until its review of that head is submitted, then answer and resolve
its comments like any other; a change goes through the
[follow-up check](#copilot-follow-up-check), still as a draft.

## Copilot follow-up check

**Copilot follow-up check** (pull requests only; after Copilot's one
review; not a review round). A change made to answer it would otherwise
reach the maintainer unreviewed. If answering Copilot needs a change:

1. Apply only the changes that answer Copilot; the pull request is still
   a draft. (If the maintainer asks for another Copilot review after the
   pull request was marked ready, convert it back to a draft first.)
2. On Claude's and Codex's pull requests, Argon checks that the whole diff
   since the approved SHA contains only those changes and each is right:
   use `.github/prompts/copilot-check.md` through the
   [README recipe](README.md#roles-and-running-the-ais), supplying the PR,
   approved and head SHAs, earlier findings and their diff in the prompt text. If the run fails
   or does not count, fall back to the AI that did not write the follow-up:
   Codex for Claude's change, Claude for Codex's. No AI checks its own change.
   Record whether Argon counted or fell back. On an Argon pull request,
   Codex checks Claude's change under the same
   [review independence rule](README.md#principles). It reports:

   ```
   **Copilot follow-up check — <Claude|Codex|Argon>** (at <SHA>)
   Compared: <approved SHA>..<SHA>
   - ✅ | ❌ Copilot comment — how it was checked
   ```

3. All pass → the [initial adversarial review](adversarial-review.md#when-it-runs)
   and, for a later follow-up, its [completion rule](adversarial-review.md#completion),
   then the remaining [delivery steps](pull-requests.md#lifecycle-of-a-pull-request).
   Copilot is not re-requested unless the maintainer asks; if Copilot
   reviews again anyway, handle it the same way once, then ask the
   maintainer. A change needing a design or
   specification decision, a failed check or a new blocking finding → ask
   the maintainer.

## Docs-only pull requests

Copilot is not requested for a pull request meeting all four conditions
(#1234):

- documentation only: Markdown files and `docs/size-exceptions.txt`;
- no gate document: no file tier A's second item in
  [`adversarial-review.md`](adversarial-review.md#which-review-runs) names;
- risk `normal` ([Risk](reviewing.md#risk));
- adversarial tier C, so a `docs/size-exceptions.txt` change beyond tier C
  (one that loosens a check) is not exempt.

Everything else in the [pull request lifecycle](pull-requests.md#lifecycle-of-a-pull-request)
and [checks](../../AGENTS.md#commands) stays. The pull request
body states that the docs-only rule applied. If Copilot reviews anyway, its
comments are handled as above. A pull request that stops meeting a
condition returns to the normal chain from that point, Copilot included.
