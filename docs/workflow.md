# AI development workflow

How work moves from an idea to `main` in ribbitto. Written for the AI tools
that do most of the work and for the maintainer who decides. `AGENTS.md`
covers how to write code; this file covers how work flows. If the two ever
disagree, fix the disagreement in a pull request.

## Principles

1. **Anything an AI produces is reviewed by a different AI before a human
   is asked to look at it.** Issues, pull requests and documents alike.
2. **The human decides at two gates and nowhere else:** approving an issue
   (`ready`) and merging a pull request. Everything between is done by the
   AIs.
3. **Process grows only from evidence.** A new rule here or in `AGENTS.md`
   must point to the AI workflow log comment (#2) that motivated it; a
   rule that never prevented anything can be removed.

## Roles

| Who | Does |
|---|---|
| Maintainer | Writes rough ideas, approves issues, merges pull requests. |
| Claude | Shapes issues, implements (mainly design-heavy work), reviews Codex's work, drives the other CLIs. |
| Codex | Shapes issues, implements (mainly well-specified work), reviews Claude's work. |
| Copilot | Reviews every pull request automatically (drafts included). Advisory. |
| Grok | Adversarial review of `high` pull requests (from M1). Advisory. |
| Antigravity | Optional: UI screenshot review, experiments, stand-in for Grok. |

Claude and Codex should end up with roughly equal shares of implementation.
If one has done the last few issues, give the next one to the other.

## Lifecycle of an issue

```
idea (maintainer, one line) or finding (AI)
  → author AI writes the issue from a template in .github/ISSUE_TEMPLATE/
  → the other AI reviews it                        (see "Reviewing")
  → author AI applies the review
  → reviewer approves → add label `ai-reviewed`
  → maintainer reads it → adds `ready`, or comments  ← human gate 1
```

- Always start from the matching template (`gh issue create --template
  <Feature|Bug|Task|Process improvement>`, or copy its headings into
  `--body-file`). Do not add, rename or drop headings.
- One issue fits one pull request of about 400 changed lines or fewer.
  Larger work is split into several issues before review.
- Put milestone issues in their GitHub milestone.
- No implementation starts on an issue without `ready`.

## Lifecycle of a pull request

```
`ready` issue
  → implementer AI works in its own worktree
    (/Users/tomoya/dev/ribbitto/wt/<name>, branch claude/<topic> or codex/<topic>)
  → draft pull request from the template, linked with "Closes #N"
  → CI and Copilot run automatically
  → the other AI reviews                            (see "Reviewing")
  → implementer applies the review (and Copilot comments it agrees with)
  → reviewer approves → label `ai-reviewed` → mark ready for review
  → Claude summarises the PR for the maintainer in Japanese
  → maintainer reviews and merges                  ← human gate 2
```

- `high` risk: the maintainer reads the whole diff; from M1, Grok also
  runs an adversarial review before the maintainer is asked.
- `normal` risk: the maintainer reads the summary and "Look here".
- All merges are manual for now. Auto-merge is added only when manual
  merging becomes a burden, with the rules recorded in the bootstrap plan
  (documentation-only changes first; head SHA checked twice).

## Reviewing

**Who reviews:** the AI that did not write it. Claude's work → Codex.
Codex's work → Claude.

**How many rounds:** at most **two** reviews by the other AI per issue or
pull request, counting re-reviews. Copilot and Grok do not count. If
round two still has blocking findings, stop and ask the maintainer.

**What to check on an issue:**
- The *why* is clear and the scope fits one pull request.
- Every *Done when* item is observable and testable; 2–5 of them.
- *Risk* is right (see below) and *Implementer* is set.
- Nothing contradicts `AGENTS.md`, `docs/`, or an earlier decision.
- Security or data-boundary concerns are called out if relevant.

**What to check on a pull request:**
- It does what the issue's *Done when* says, and nothing unrelated.
- Correctness, error handling, organisation scoping, authorization.
- Layering: no import that breaks the dependency direction in `AGENTS.md`.
- Tests prove the behaviour (authorization: someone who must not see the
  data does not see it). No test was deleted, skipped or weakened.
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

## Risk

A change is **high** risk if it touches any of: `internal/app/authz/**`,
`internal/app/auth/**`, `internal/web/middleware/**`,
`internal/realtime/**`, `db/migrations/**`, `db/queries/**`,
`.github/**`, `scripts/**`, `tools/**`, `Makefile`, `.golangci.yml`,
`sqlc.yaml`, `go.mod`, `go.sum`, `docs/workflow.md`, or any `AGENTS.md`
or `CLAUDE.md`. Everything else is **normal**. The author states the risk
in the template; the reviewer checks it.

## Running the other AI

Headless CLIs can wait forever for input. **Always close stdin and set a
time limit.** macOS has no `timeout`, so use Perl's `alarm`:

```sh
limit() { perl -e 'alarm shift; exec @ARGV' "$@"; }   # limit 900 cmd args…
```

Codex reviews Claude's work:

```sh
# Issue (read-only; the issue is piped in, so stdin is not left open)
gh issue view N --json title,body,comments \
  | limit 900 codex exec -s read-only -o review.md \
    "Review this GitHub issue following docs/workflow.md#reviewing. Reply in the report format."

# Pull request (in a clean worktree at the PR head, after git fetch)
limit 1800 codex exec review --base origin/main \
  "Also follow docs/workflow.md#reviewing. Reply in the report format." < /dev/null
```

Claude reviews Codex's work: the orchestrating Claude session reviews
directly, or `limit 1800 claude -p "…" < /dev/null`.

Post the result with `gh issue comment` / `gh pr comment`. Do not rely on
GitHub's `@codex review`: it looks only at the most severe problems, so it
does not replace this review.

## Keeping state

- **Status (#1):** read it at the start of a session. At the end of a unit
  of work, overwrite its body using its headings (under ~30 lines;
  replace, don't append).
- **AI workflow log (#2):** when something goes wrong with the workflow —
  a tool hangs, a review misses something, work is redone, an instruction
  is misunderstood — add one comment: what happened · cost · likely cause
  · (optional) idea. No secrets or personal data.
- **Retrospective:** at the end of each milestone, or after about ten new
  log comments. An AI groups the comments since the last `Retrospective:`
  comment, proposes the smallest fixes (mechanical first, removal before
  addition), the maintainer picks, accepted ones become `process` issues,
  and a `Retrospective:` summary comment closes the round.
- **Roadmap:** `docs/roadmap.md`, updated when a milestone ends or the
  plan changes.

## Labels

| Label | Set by | Meaning |
|---|---|---|
| `ai-reviewed` | the orchestrating AI, after the reviewer approves | Ready for the maintainer to look at. |
| `ready` | maintainer only | Issue approved; implementation may start. |
| `process` | template | Workflow improvement. |
| `bug` | template | Something is broken. |
