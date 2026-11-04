Read when: orchestrators handle review findings or later pushes; implementers apply fixes and eligible reviewers check follow-ups or closure.

# Review follow-ups

## Copilot follow-up check

PRs only, after a fix for a Copilot or [third-AI](review-operations.md#third-ai-review)
(adversarial) finding, for final-SHA re-confirmation of a maintainer-accepted
conversion-parity difference when the head later changed, or after the two-run
third-AI cap for a maintainer-selected fix or a limited merge of `main`
([third-AI review](review-operations.md#third-ai-review));
not a review round or a third-AI run, so the two-round cross-review limit and
the two-run third-AI cap are untouched. If answering needs a change (or a
post-cap delta or accepted-difference re-confirmation must be checked):

1. Apply only changes answering findings of this round (Copilot's and parallel
   review findings fixed in the follow-up commits, or a fix the maintainer
   selected after the third-AI cap), each named, plus any clean merge of `main`
   eligible for a limited check after the third-AI cap. Each fix for a
   third-AI finding includes a regression test that fails without it; only when
   no automated test can reproduce it (a rule/doc fix, or a race no existing
   harness can gate; never effort or time) record why and the alternative
   verification: a scripted/manual reproduction, a mutation showing the guard
   matters, or the exact text change. Keep the PR a draft (convert a ready PR
   back to a draft until the follow-up check or escalation completes, including
   if the maintainer asked for another Copilot review or a post-cap change after
   ready).
2. An AI that did not write the change ([review independence](README.md#principles))
   checks that the whole diff since the baseline SHA (the latest SHA whose
   change scope is already covered by a review or an independent follow-up
   check — the head the round reviewed, or, after the two-run third-AI cap, the
   latest SHA covered by run 2 or by an approved post-cap follow-up check; the
   approval record of a maintainer's acceptance at a SHA and the review coverage
   of a change scope are different concepts, and no part of the change set may
   be left unverified — no gap — so the head SHA at the time of an acceptance
   may be used as the comparison start only if the changes up to it were
   themselves verified/covered, never unconditionally: for example, if a review
   completes at `A`, extra changes and a maintainer's acceptance occur at `B`,
   and the final head is `C`, then if `B` is unverified the check must cover
   `A..C`, the whole unverified change set, not just `B..C`, while keeping the
   two-completed-runs third-AI cap with no new third-AI review started by this
   rule) contains only
   those allowed changes, each fix answers its finding, and for each third-AI
   finding the test fails without it (by running it or reading a CI run without
   the fix) — or that the recorded reason holds and the alternative
   verification was done (an unaccepted reason is ❌) — and, after the two-run
   third-AI cap, that the delta is limited (changing no behaviour, security
   boundary or interaction with existing code beyond answering the named
   findings; a clean merge of `main` may be classified as limited only after
   establishing that it leaves the PR's behaviour, security boundaries and
   interactions unchanged, including effects through callers, direct or
   transitive dependencies and shared contracts, with any uncertainty or
   substantive change marked ❌ for the maintainer) and, whenever the head
   changed after a conversion-parity difference was accepted (before or after
   the cap), that the accepted occurrence conditions and effect are unchanged
   at the final SHA, using `.github/prompts/copilot-check.md` via the
   [README recipe](README.md#roles-and-running-the-ais) with
   [review context](review-operations.md#running-a-review). Header: `Copilot`
   only for Copilot findings, else `Adversarial`. Use
   [failure rules](delivery.md#quota-substitution); record the AI:

   ```
   **<Copilot|Adversarial> follow-up check — <Claude|Codex|Argon>** (at <SHA>)
   Compared: <baseline SHA>..<SHA>
   - ✅ | ❌ finding or post-cap item — how it was checked (for a third-AI finding: the fix, and the test that fails without it, or no test — the reason and alternative verification)
   ```

3. All pass → if the [third-AI review](review-operations.md#third-ai-review) is
   required and no completed run exists yet, run the first over the PR's whole
   diff at the new head; if one completed run exists and the diff since it
   touches the third-AI areas, run the second on
   `Compared: <first run's SHA>..<head>` (a push before it restarts this check);
   if two completed runs already exist, do not run a third adversarial review;
   then the remaining [lifecycle steps](README.md#lifecycle) once the required
   reviews and this check cover the final head. Do not re-request Copilot unless the maintainer asks; if it reviews
   again anyway, handle once the same way, then ask the maintainer. Ask the
   maintainer if a fix itself needs a fix, if a post-cap change substantively
   alters behaviour, a security boundary or the interaction with existing code
   (this escalation takes precedence even when the change solely answers a
   named finding or implements a maintainer-selected fix;
   [post-cap rules](review-operations.md#third-ai-review)), if an accepted
   parity difference's occurrence conditions or effect changed, or for any
   other change in the diff, design/spec decision, failed check or new blocking
   finding.

## Adversarial follow-up check

Pull requests only; after a fix for a finding of the adversarial review, for
final-SHA re-confirmation of a maintainer-accepted conversion-parity difference
when the head later changed, or after the two-run third-AI cap for a
maintainer-selected fix or a limited merge of `main`
([third-AI review](review-operations.md#third-ai-review)); not a
review round or a third-AI run, so the two-round cross-review limit and the
two-run third-AI cap are untouched. It follows the
[follow-up check rule and `copilot-check.md` prompt](review-followups.md#copilot-follow-up-check)
(the report header says `Copilot` only when a Copilot finding is covered,
otherwise `Adversarial`).

## Closure verification

**Closure verification** (pull requests only; after the two-round limit;
at most once per PR; not a review round; keeps its round-two reviewer). Evidence: #2, the case of #14.

1. **Eligibility is declared in the round-2 report,** after the round-two
   review described in
   [Review independence and rounds](reviewing.md#review-independence-and-rounds)
   and [Pull request cross-review](reviewing.md#pull-request-cross-review), and
   only if every remaining blocking finding
   is mechanical: the report names the exact fix, the reviewer has
   verified that the fix works, and it needs no design or specification
   decision. Without that declaration, ask the maintainer.
2. The implementer applies exactly those fixes and nothing else.
3. The reviewer checks that the whole diff since the round-2 SHA contains
   only the named fixes, then re-runs the failing check or live test for
   each finding — not the rest of the PR — and reports:

   ```
   **Closure verification — <Claude|Codex|Argon>** (at <SHA>)
   Compared: <round-2 SHA>..<SHA>
   - ✅ | ❌ finding — how it was checked
   ```

4. All pass → the remaining [pull request lifecycle](README.md#lifecycle)
   after cross-review approval. Any other change in the diff, a failed
   check, a new blocking finding or anything needing a decision → ask the
   maintainer.

## Follow-up pushes

**Before every follow-up push** — a Copilot or adversarial follow-up, a fix
for a failing check, a merge of `main` — run `make prepush`
([Checks](../../README.md#checks)) and push only if it passes: it regenerates
files and checks the manifests, document sizes and lint, keeping each exit
status. Ad-hoc command chains have hidden a failure before (#848).

A later merge of `main` to resolve conflicts is reported in a PR comment
listing the files and how they were resolved; if the resolution does more
than combine both sides, it needs a normal review. A combination that
changes lock or transaction order or the delivery contract (the third-AI review list) does more than combine, even when every line came from one side. Either way, follow the
[third-AI review rules](review-operations.md#third-ai-review), counting this pull
request's own commits since the last adversarially reviewed head, not only
the resolution. After the two-run third-AI cap there is no third adversarial
run: a merge of `main` that qualifies as limited gets the independent
[follow-up check](#copilot-follow-up-check), while one that does more than
combine, changes lock or transaction order or the delivery contract, or is
otherwise substantive goes to the maintainer under the
[post-cap escalation rules](review-operations.md#third-ai-review).

**A typo, or a rewording that changes no meaning,** may be pushed without
a follow-up check, and does not invalidate a review. These are not that, and
get the check (and a re-review where they invalidate one): a change to code
behaviour, to a test or CI condition, to safety (authentication,
authorisation, organisation scoping), to the meaning of a workflow or
specification, or anything else that bears on a review decision. A nit need
not be fixed, and is not pushed on its own.

Which pull requests skip Copilot:
[docs-only pull requests](review-operations.md#docs-only-pull-requests).

