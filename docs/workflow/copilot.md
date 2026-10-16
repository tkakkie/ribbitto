# Copilot

What follows Copilot's one review of a pull request, and when Copilot is
not requested. The other review rules are in [`reviewing.md`](reviewing.md).

**Copilot follow-up check** (pull requests only; after Copilot's one
review; not a review round). Copilot is requested by hand on the draft
after the other AI approved, so a change made to answer it would otherwise
reach the maintainer unreviewed. If answering Copilot needs a change:

1. Apply only the changes that answer Copilot; the pull request is still
   a draft. (If Copilot is re-requested after the pull request was marked
   ready, convert it back to a draft first.)
2. The AI that approved the pull request checks that the whole diff since
   the SHA it approved contains only those changes and that each one is
   right. On an Argon trial pull request, Codex checks a change Claude
   wrote, from CI and a diff on stdin, never in Argon's tree
   ([who does what](argon-implementer.md#who-does-what)). It reports:

   ```
   **Copilot follow-up check — <Claude|Codex>** (at <SHA>)
   Compared: <approved SHA>..<SHA>
   - ✅ | ❌ Copilot comment — how it was checked
   ```

3. All pass → the initial adversarial review runs on the new head, at the
   pull request's tier recomputed over its whole diff, still as a draft,
   with every finding disposed of; a follow-up after that review needs it
   again only if the follow-up's diff matches tier A or B, by the reviewer
   the [completion rule](adversarial-review.md#which-review-runs) names. Then
   `ai-reviewed`, ready for review and the normal merge decision. Copilot is not
   re-requested; if Copilot reviews again anyway, handle it the same way
   once, then ask the maintainer. A change needing a design or
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

Everything else stays: the cross-review (at most two rounds, closure
verification), `make check` and CI, `ai-reviewed` after the reviewer
approves, and the maintainer's merge decision. The pull request
body states that the docs-only rule applied. If Copilot reviews anyway, its
comments are handled as above. A pull request that stops meeting a
condition returns to the normal chain from that point, Copilot included.
