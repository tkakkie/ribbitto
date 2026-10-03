# Reviewing

**Who reviews:** for an issue, the AI that did not write the issue; for a
pull request, the AI that did not implement it. Claude's work → Codex;
Codex's work → Claude.

**How many rounds:** at most **two** reviews by the other AI per issue or
pull request, counting re-reviews. Copilot and Grok do not count. If
round two still has blocking findings, a pull request may go through
*closure verification* (below) when it qualifies; in every other case,
stop and ask the maintainer.

**What to check on an issue:**
- The *why* is clear and the scope fits one pull request.
- Every *Done when* item is observable and testable; 2–5 of them.
- *Risk* is right (see below) and *Implementer* is set.
- Nothing contradicts `AGENTS.md`, `docs/`, or an earlier decision in
  `docs/decisions/` (indexed by [`DECISIONS.md`](../../DECISIONS.md)).
- Security or data-boundary concerns are called out if relevant.

**What to check on a pull request:**
- It does what the issue's *Done when* says, and nothing unrelated.
- Correctness, error handling, organisation scoping, authorization.
- Layering: no import that breaks the dependency direction in `AGENTS.md`.
- Boundaries: code sits in the feature the issue names; it uses other
  features only through their exported API and writes only its own
  tables or a listed exception (feature map in
  `docs/architecture/features.md`); every new edge in
  `docs/dependencies.md` is explained in the PR.
- Tests prove the behaviour (authorization: someone who must not see the
  data does not see it). No test was deleted, skipped or weakened.
- A test-only pull request meant to catch a regression records, in
  *Verified by*, one deliberate defect in the code under test that breaks
  the behaviour the test guards and fails its assertion (not a crash or a
  compile error), and that the test passes again once it is reverted
  (#360).
- Comments explain *why*; `docs/` is updated when terminology,
  invariants, data flow or dependencies change.

**How to report:** one comment on the issue or pull request:

```
**AI review — <Codex|Claude>, round <1|2>** (for a PR: at <short SHA>)
Verdict: approve | changes requested
- [blocking] file:line or section — problem — suggested fix
- [nit] …
```

"Approve" with no findings is a valid review. Only `[blocking]` findings
hold things up.

**Closure verification** (pull requests only; after the two-round limit;
at most once per PR; not a review round). Evidence: #2, the case of #14.

1. **Eligibility is declared in the round-2 report,** after a normal
   review of that revision, and only if every remaining blocking finding
   is mechanical: the report names the exact fix, the reviewer has
   verified that the fix works, and it needs no design or specification
   decision. Without that declaration, ask the maintainer.
2. The implementer applies exactly those fixes and nothing else.
3. The reviewer checks that the whole diff since the round-2 SHA contains
   only the named fixes, then re-runs the failing check or live test for
   each finding — not the rest of the PR — and reports:

   ```
   **Closure verification — <Claude|Codex>** (at <SHA>)
   Compared: <round-2 SHA>..<SHA>
   - ✅ | ❌ finding — how it was checked
   ```

4. All pass → the remaining steps (Grok when
   [required](adversarial-review.md#when-grok-runs), then
   `ai-reviewed` and ready for review, then Copilot) and the normal merge
   decision. Any other change
   in the diff, a failed check, a new blocking finding or anything needing
   a decision → ask the maintainer.

**Copilot follow-up check** (pull requests only; after Copilot's one
review; not a review round). Copilot reviews after the other AI approved,
so a change made to answer it would otherwise reach the maintainer
unreviewed. If answering Copilot needs a change:

1. Convert the pull request back to a draft and apply only the changes
   that answer Copilot.
2. The AI that approved the pull request checks that the whole diff since
   the SHA it approved contains only those changes and that each one is
   right, and reports:

   ```
   **Copilot follow-up check — <Claude|Codex>** (at <SHA>)
   Compared: <approved SHA>..<SHA>
   - ✅ | ❌ Copilot comment — how it was checked
   ```

3. All pass → mark it ready for review again and the normal merge
   decision. Grok does not run again and Copilot is not re-requested; if
   Copilot reviews again anyway, handle it the same way once, then ask the
   maintainer. A change needing a design or specification decision, a
   failed check or a new blocking finding → ask the maintainer.

A later merge of `main` to resolve conflicts is reported in a PR comment
listing the files and how they were resolved; if the resolution does more
than combine both sides, it needs a normal review.

## Risk

A change is **high** risk if it touches any of: `internal/app/authz/**`,
`internal/app/auth/**`, `internal/web/middleware/**`,
`internal/realtime/**`, `db/migrations/**`, `db/queries/**`,
`.github/**`, `scripts/**`, `tools/**`, `Makefile`, `.golangci.yml`,
`sqlc.yaml`, `go.mod`, `go.sum`, `docs/workflow/**`, or any `AGENTS.md`
or `CLAUDE.md`. Everything else is **normal**. The author states the risk
in the template; the reviewer checks it. Whether Grok runs is decided
separately, by what the change alters
([`adversarial-review.md`](adversarial-review.md#when-grok-runs)).
