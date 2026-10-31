# Reviewing

## Review independence and rounds

**Who reviews:** an AI that neither wrote nor changed the work, decided from
the actual history ([review independence](README.md#principles)): for an
issue, an AI other than its writer; for a pull request, an AI other than the
implementer and anyone who pushed to it. Copilot follow-up checks use
[an AI that did not write the change](delivery.md#copilot-follow-up-check).
A failed, timed-out or empty run is handled as the README says. If independence
cannot be kept, ask the maintainer.

**Who changed it** is read from the orchestration record: the issue or pull
request body and comments (process notes included), the review reports, the
branch prefix (`claude/`, `codex/`, `argon/`) and commit trailers where
present. GitHub's Author, Committer and token name are not evidence: every AI
acts as the same account. If it cannot be told who wrote or changed the work,
do not guess a reviewer: ask the maintainer.

**How many rounds:** at most **two** reviews, by AIs eligible under *Who reviews*, per issue or
pull request, counting re-reviews together.
Copilot and the adversarial review do not count. If round two still has
blocking findings, a pull request may go through *closure verification* (below) when it qualifies; in every other
case, stop and ask the maintainer.

**Round two:** for a pull request, supply `Compared: <round-1 SHA>..<head>`
and centre the review on that delta: verify that each round-1 blocking
finding is resolved and that the delta adds no new problem, including its
effect on unchanged code and the issue's *Done when*. Re-read the rest only
where the delta affects it. An issue's round two remains a full re-read;
issue revisions have no SHA.

## Issue review

**What to check on an issue:**
- The *why* is clear and the scope fits one pull request.
- Every *Done when* item is observable and testable; 2–5 of them.
- *Risk* is right (see below) and any *Implementer* the maintainer names is stated; otherwise the
  [README default](README.md#roles-and-running-the-ais) applies.
- Nothing contradicts `AGENTS.md`, `docs/`, or an earlier decision in
  `docs/decisions/` (indexed by [`DECISIONS.md`](../../DECISIONS.md)).
- Security or data-boundary concerns are called out if relevant.

## Pull request review

**What to check on a pull request:**
- It does what the issue's *Done when* says, and nothing unrelated.
- Correctness, error handling, organisation scoping, authorization.
- Layering: no import that breaks the dependency direction in `AGENTS.md`.
- Boundaries: code sits in the feature the issue names; it uses other
  features only through their exported API and writes only its own
  tables or a listed exception (feature map in
  `docs/architecture/features.md`); every new edge in
  `docs/dependencies.md` is explained in the PR. The CI run's summary lists
  added and removed edges, crossing units first; added edges also appear
  as notices on `docs/dependencies.md`. Authors can run
  `go -C tools run ./deps --diff origin/main ..` locally for the same list.
- Tests prove the behaviour (authorization: someone who must not see the
  data does not see it). No test was deleted, skipped or weakened.
- A test-only pull request meant to catch a regression records, in
  *Verified by*, one deliberate defect in the code under test that breaks
  the behaviour the test guards and fails its assertion (not a crash or a
  compile error), and that the test passes again once it is reverted
  (#360). Evidence may come from local checks or CI; record the deliberate
  defect and its failing assertion, then revert it.
- Comments explain *why*; `docs/` is updated when terminology,
  invariants, data flow or dependencies change.
- For each exception in `docs/language-exceptions.txt`, verify that its
  approving comment is the maintainer's and approves that exact path.

## Reporting

**How to report:** one comment on the issue or pull request:

```
**AI review — <Codex|Claude|Argon>, round <1|2>** (for a PR: at <short SHA>)
Compared: <round-1 SHA>..<head> (PR round 2 only)
Verdict: approve | changes requested
- [blocking] file:line or section — problem — suggested fix
- [nit] …
```

"Approve" with no findings is a valid review. Only `[blocking]` findings
hold things up. Copilot follow-up checks use the kept `copilot-check.md`
prompt, run by an AI that did not write the change
([delivery](delivery.md#copilot-follow-up-check)); a failed or non-counting
run is retried as the README says. Adversarial follow-up checks and closure
verification keep their existing reviewers.

## Closure verification

**Closure verification** (pull requests only; after the two-round limit;
at most once per PR; not a review round). Evidence: #2, the case of #14.

1. **Eligibility is declared in the round-2 report,** after the round-two
   review described above, and only if every remaining blocking finding
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

**Copilot follow-up check:** in [`delivery.md`](delivery.md#copilot-follow-up-check).

## Adversarial follow-up check

**Adversarial follow-up check** (pull requests only; after a fix for a
finding of the adversarial review; not a review round, so the two-round
limit is untouched). A fix to an adversarial finding would otherwise
reach the re-review without either AI checking it:

1. Apply only the fix, with a regression test that fails without it; the
   pull request is a draft. Only when no automated test can reproduce the
   finding (a rule or documentation fix, or a race no existing harness can
   gate) record instead why, and the alternative verification done: a
   scripted or manual reproduction, a mutation showing the guard matters,
   or the exact text change. Effort or time is never such a reason.
2. The AI that did not write the fix (following [review independence](README.md#principles)) checks
   that the diff since the reviewed head contains only that fix,
   that it answers the original finding, and that the test fails without
   it (by running it, or by reading a run without the fix in CI) — or
   that the recorded reason holds and the alternative verification
   was done; a reason it does not accept is ❌. It reports:

   ```
   **Adversarial follow-up check — <Claude|Codex|Argon>** (at <SHA>)
   Compared: <reviewed SHA>..<SHA>
   - ✅ | ❌ finding — the fix, and the test that fails without it
     (or: no test — the reason, and the alternative verification)
   ```

3. All pass → the re-review the
   [third-AI review rules](delivery.md#third-ai-review) names, at the
   checked SHA; a push before it starts this check again. Any other change,
   a failed check, or a fix needing a design or specification decision →
   ask the maintainer.

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
[third-AI review rules](delivery.md#third-ai-review), counting this pull
request's own commits since the last adversarially reviewed head, not only
the resolution.

**A typo, or a rewording that changes no meaning,** may be pushed without
a follow-up check, and does not invalidate a review. These are not that, and
get the check (and a re-review where they invalidate one): a change to code
behaviour, to a test or CI condition, to safety (authentication,
authorisation, organisation scoping), to the meaning of a workflow or
specification, or anything else that bears on a review decision. A nit need
not be fixed, and is not pushed on its own.

Which pull requests skip Copilot: [docs-only pull requests](delivery.md#docs-only-pull-requests).

## Risk

A change is **high** risk if it touches any of: `internal/org/**`,
`internal/identity/**`, `internal/web/middleware/**`, the authentication
entry points `internal/web/org.go`, `internal/web/signin.go`,
`internal/web/signup.go` and `internal/web/setup.go`,
`internal/realtime/**`, `db/migrations/**`, `db/queries/**`,
`.github/**`, `tools/**`, `Makefile`, `.golangci.yml`,
`sqlc.yaml`, `go.mod`, `go.sum`, `docs/workflow/**`, or any `AGENTS.md`
or `CLAUDE.md`. Everything else is **normal**. The author states the risk
in the template; the reviewer checks it. Which adversarial review runs is
decided separately, by what the change alters
([third-AI review](delivery.md#third-ai-review)).
