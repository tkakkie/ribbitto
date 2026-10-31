# Reviewing

## Review independence and rounds

**Who reviews:** following [review independence](README.md#principles),
Claude-written issues → Argon through the [README recipe](README.md#roles-and-running-the-ais),
falling back to Codex when the run does not count; other Claude work → Codex;
Codex's work → Claude; Argon's work → Claude, and a fix Claude writes
there → Codex. Copilot follow-up checks use
[Argon and an independent fallback](copilot.md#copilot-follow-up-check).
Claude's own work is its issues, the fixes it writes during a review, and
an issue the maintainer named Claude to implement.

**How many rounds:** at most **two** reviews by the other AI per issue or
pull request, counting re-reviews and Argon and fallback Codex together.
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
- *Risk* is right (see below) and *Implementer* is set by the maintainer
  under the [README's roles](README.md#roles-and-running-the-ais).
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
hold things up. For each Claude-written issue review, the orchestrator
notes in its review comment whether Argon counted or fell back to Codex.
Argon also runs the kept `copilot-check.md` prompt through the README
recipe on Claude's and Codex's pull requests; a failed or non-counting run falls back to the AI that did not
write the change (Codex for Claude's, Claude for Codex's), as in
[Copilot follow-up](copilot.md#copilot-follow-up-check). Adversarial follow-up
checks and closure verification keep their existing reviewers.

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
   each finding — not the rest of the PR — under the
   [handoff rule](running-other-ai.md#handoff-of-another-ais-code), and reports:

   ```
   **Closure verification — <Claude|Codex>** (at <SHA>)
   Compared: <round-2 SHA>..<SHA>
   - ✅ | ❌ finding — how it was checked
   ```

4. All pass → the remaining [pull request lifecycle](pull-requests.md#lifecycle-of-a-pull-request)
   after cross-review approval. Any other change in the diff, a failed
   check, a new blocking finding or anything needing a decision → ask the
   maintainer.

**Copilot follow-up check:** in [`copilot.md`](copilot.md).

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
2. The AI that did not write the fix (Claude ↔ Codex; Codex on an Argon
   pull request, following [review independence](README.md#principles)) checks
   that the diff since the reviewed head contains only that fix,
   that it answers the original finding, and that the test fails without
   it (where the
   [handoff rule](running-other-ai.md#handoff-of-another-ais-code) keeps
   it from running the test, it reads a
   run without the fix in CI or in the implementer's own sandbox) — or
   that the recorded reason holds and the alternative verification
   was done; a reason it does not accept is ❌. It reports:

   ```
   **Adversarial follow-up check — <Claude|Codex>** (at <SHA>)
   Compared: <reviewed SHA>..<SHA>
   - ✅ | ❌ finding — the fix, and the test that fails without it
     (or: no test — the reason, and the alternative verification)
   ```

3. All pass → the re-review the
   [completion rule](adversarial-review.md#completion) names, at the
   checked SHA; a push before it starts this check again. Any other change,
   a failed check, or a fix needing a design or specification decision →
   ask the maintainer.

## Follow-up pushes

**Before every follow-up push** — a Copilot or adversarial follow-up, a fix
for a failing check, a merge of `main` — run `make prepush`
([Checks](../../README.md#checks)) and push only if it passes: it regenerates
files and checks the manifests, document sizes and lint, keeping each exit
status. Ad-hoc command chains have hidden a failure before (#848). The
[handoff exception](running-other-ai.md#handoff-of-another-ais-code) applies.

A later merge of `main` to resolve conflicts is reported in a PR comment
listing the files and how they were resolved; if the resolution does more
than combine both sides, it needs a normal review. A combination that
changes lock or transaction order or the delivery contract (tier A's first
item) does more than combine, even when every line came from one side. Either way, follow the
[completion rule](adversarial-review.md#completion), counting this pull
request's own commits since the last adversarially reviewed head, not only
the resolution.

Which pull requests skip Copilot: [docs-only pull requests](copilot.md#docs-only-pull-requests).

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
([`adversarial-review.md`](adversarial-review.md#which-review-runs)).
