Read when: orchestrators handle review findings or later pushes; implementers apply fixes and eligible reviewers check follow-ups or closure.

# Review follow-ups

## Copilot follow-up check

PRs only, after a Copilot or [third-AI](review-operations.md#third-ai-review) (adversarial) fix (not a review round; two-round limit untouched). If answering needs a change:

1. Apply only changes answering this round's findings (Copilot and parallel-review fixes in follow-up commits), each named. Each third-AI fix includes a regression test failing without it; only if no automated test can reproduce it (rule/doc fix or a race no existing harness can gate; never effort/time) record why and alternative verification (scripted/manual reproduction, mutation showing the guard matters or exact text change). Keep the PR draft (re-draft first if the maintainer asked for another Copilot review after ready).
2. An AI that did not write the change ([review independence](README.md#principles)) checks via `.github/prompts/copilot-check.md` ([README recipe](README.md#roles-and-running-the-ais), [review context](review-operations.md#running-a-review)) that the whole diff since the baseline SHA (the round's reviewed head) contains only those changes, each answers its finding, and each third-AI fix's test fails without it (running it or reading a pre-fix CI run) — or the no-test reason holds and alternative verification was done (unaccepted reason: ❌). Header: `Copilot` only if a Copilot finding is covered, else `Adversarial`; retry non-counting runs once per README; record who ran it:

   ```
   **<Copilot|Adversarial> follow-up check — <Claude|Codex|Argon>** (at <SHA>)
   Compared: <baseline SHA>..<SHA>
   - ✅ | ❌ finding — how checked (third-AI: fix + test failing without it, or no-test reason + alternative verification)
   ```

3. All pass → run [third-AI review](review-operations.md#third-ai-review) on the whole PR diff at the new head when required and no adversarial report covers that head, or repeat it under the [later-push rule](review-operations.md#third-ai-review) if an earlier report exists (a push before it restarts this check), then complete the lifecycle. Do not re-request Copilot unless the maintainer asks (if it reviews again anyway, handle once as above, then ask the maintainer). Any other diff change, design/spec decision, failed check or new blocking finding → ask the maintainer.

## Adversarial follow-up check

Pull requests only; after a fix for a finding of the adversarial review; not a
review round, so the two-round limit is untouched. It follows the
[follow-up check rule and `copilot-check.md` prompt](review-followups.md#copilot-follow-up-check)
(the report header says `Copilot` only when a Copilot finding is covered,
otherwise `Adversarial`).

## Closure verification

**Closure verification** (pull requests only; after the two-round limit;
at most once per PR; not a review round; keeps its round-two reviewer). Evidence: #2, the case of #14.

1. **Eligibility is declared in the round-2 report,** after the round-two
   review described in [Review independence and rounds](reviewing.md#review-independence-and-rounds) and [Pull request cross-review](reviewing.md#pull-request-cross-review), and only if every remaining blocking finding
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
the resolution.

**A typo, or a rewording that changes no meaning,** may be pushed without
a follow-up check, and does not invalidate a review. These are not that, and
get the check (and a re-review where they invalidate one): a change to code
behaviour, to a test or CI condition, to safety (authentication,
authorisation, organisation scoping), to the meaning of a workflow or
specification, or anything else that bears on a review decision. A nit need
not be fixed, and is not pushed on its own.

Which pull requests skip Copilot: [docs-only pull requests](review-operations.md#docs-only-pull-requests).

