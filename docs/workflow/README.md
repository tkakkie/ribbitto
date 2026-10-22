# AI development workflow

How work moves from idea to `main` in ribbitto, for AIs and the maintainer.
`AGENTS.md` covers coding; these files cover workflow. Correct any
disagreement in a pull request.

## Index

Start with your role: [Codex implementer](#implementer-codex),
[Argon implementer](#implementer-argon), [reviewer](#reviewer), or
[orchestrator/handoff](#orchestrator-and-handoff).

| File | Covers |
|---|---|
| This file | Principles, role entries, issues, state, audit, labels |
| [Pull requests](pull-requests.md#lifecycle-of-a-pull-request) | Delivery and merge approval |
| [Reviewing](reviewing.md#review-independence-and-rounds) | Cross-review, closure, follow-up, risk |
| [Copilot](copilot.md#copilot) | Requesting, follow-up, docs-only exception |
| [Adversarial review](adversarial-review.md#adversarial-review) | Tiers and completion |
| [Running Argon](running-argon.md), [agent](argon-review-agent.md) | Reviews |
| [Running the other AI](running-other-ai.md#running-the-other-ai) | Headless runs, Codex setup, handoff |
| [Argon implementer](argon-implementer.md#argon-as-implementer) | Eligibility, boundary, preflight, records |
| [Argon verify](argon-verify.md) | Permissions, container, results |
| [Verify operation](argon-verify-operation.md) | Switch and checks |
| [Sweep](argon-verify-sweep.md) | Leftovers |
| [Argon pull requests](argon-pull-requests.md#argons-pull-requests) | Review differences and mutation evidence |
| [Investigations](investigations.md#investigations-and-the-performance-target) | Approval, caps, target, host measurements |

## Principles

1. **Every issue and every pull request an AI writes is reviewed by an AI
   that did not write it before the maintainer is asked to look at it** —
   including any documentation the pull request changes. Review reports,
   Status updates, workflow-log comments and chat messages are not
   reviewed.
2. **The maintainer decides at two normal delivery gates:** approving an
   issue (`ready`) and [merging a pull request](pull-requests.md#lifecycle-of-a-pull-request).
   Everything between is done by the AIs. The linked merge rule covers AI execution.
   The maintainer is also asked when a review does not converge (see
   [Reviewing](reviewing.md)) and when choosing retrospective proposals.
3. **Process grows only from evidence.** From now on, a new process rule
   comes from a `process` issue whose *Problem* section links the AI
   workflow log comments (#2) that motivated it; a rule that never
   prevented anything can be removed. Such rules live here and need no
   decision record, unless they conflict with an existing decision
   ([`DECISIONS.md`](../../DECISIONS.md)).

[Investigations](investigations.md).

## Roles

The maintainer writes rough ideas, approves issues and decides merges.
Claude writes and details issues, orchestrates the other CLIs and handles
handoffs. Codex also writes issues. Review assignments are in
[reviewing](reviewing.md#review-independence-and-rounds);
implementation assignments are in [eligibility](argon-implementer.md#which-issues).

### Shared gates

[Principles](#principles), [issue gate](#lifecycle-of-an-issue),
[delivery and merge approval](pull-requests.md#lifecycle-of-a-pull-request),
[review independence and rounds](reviewing.md#review-independence-and-rounds),
[risk](reviewing.md#risk), [adversarial tiers](adversarial-review.md#which-review-runs)
and [handoff rule](running-other-ai.md#handoff-of-another-ais-code).

### Implementer Codex

- [Shared gates](#shared-gates).
- [Assignment](argon-implementer.md#which-issues), [permissions](running-other-ai.md#codex-permissions),
  [implementation brief](running-other-ai.md#implementation-brief).
- For fixes: [closure verification](reviewing.md#closure-verification),
  [adversarial follow-up](reviewing.md#adversarial-follow-up-check),
  [follow-up pushes](reviewing.md#follow-up-pushes),
  [Copilot](copilot.md#copilot-follow-up-check) and
  [completion](adversarial-review.md#completion).
- [State](#keeping-state); [Investigations](investigations.md#investigations-and-the-performance-target) when needed.

### Implementer Argon

- [Shared gates](#shared-gates).
- [Start gate, eligibility, permissions, preflight and records](argon-implementer.md#argon-as-implementer);
  [verification](argon-verify.md).
- For fixes: [closure verification](reviewing.md#closure-verification),
  [adversarial follow-up](reviewing.md#adversarial-follow-up-check),
  [Copilot](copilot.md#copilot-follow-up-check) and
  [completion](adversarial-review.md#completion).
- [State](#keeping-state); [Investigations](investigations.md#investigations-and-the-performance-target) when needed.

### Reviewer

- [Shared gates](#shared-gates), [reporting](reviewing.md#reporting).
- [Issue checklist](reviewing.md#issue-review) or
  [pull request checklist](reviewing.md#pull-request-review).
- [Closure](reviewing.md#closure-verification),
  [adversarial follow-up](reviewing.md#adversarial-follow-up-check),
  [follow-up pushes](reviewing.md#follow-up-pushes),
  [Copilot](copilot.md#copilot-follow-up-check) and
  [completion](adversarial-review.md#completion).
- [Headless reviews](running-other-ai.md#headless-reviews),
  [context](running-other-ai.md#review-context).
- For Argon's work: [roles](argon-implementer.md#who-does-what),
  [review differences](argon-pull-requests.md#who-reviews),
  [invocation](argon-pull-requests.md#the-fixed-invocation),
  [mutation evidence](argon-pull-requests.md#mutation-evidence).
- For trusted Argon reviews: [launcher permissions](running-argon.md#the-boundary).
  [State](#keeping-state).

### Orchestrator and handoff

- [Shared gates](#shared-gates), [state](#keeping-state), [labels](#labels) and [investigations](investigations.md#investigations-and-the-performance-target).
- [Review checks, reporting, closure and follow-ups](reviewing.md#reviewing).
- [Copilot](copilot.md#copilot), [adversarial review](adversarial-review.md#adversarial-review).
- [Headless reviews](running-other-ai.md#headless-reviews),
  [Codex permissions and brief](running-other-ai.md#running-codex).
- For Argon: [eligibility, permissions, preflight and records](argon-implementer.md#argon-as-implementer),
  [pull requests](argon-pull-requests.md#argons-pull-requests),
  [review launcher](running-argon.md#running-argon), [verify](argon-verify.md).
- Optional: [audit](#occasional-audit).

- Mechanical subagent chores (waiting for CI, merge preparation, conflict
  resolution, Status updates) may run on a smaller model.
- Parallel work is not capped; avoid pull requests that touch the same files.

## Lifecycle of an issue

```
idea (maintainer, one line) or finding (AI)
  → author AI writes the issue from a template in .github/ISSUE_TEMPLATE/
  → the AI that did not write the issue reviews it  (see reviewing.md)
  → author AI applies the review
  → reviewer approves → add label `ai-reviewed`
  → maintainer reads it → adds `ready`, or comments  ← gate 1
```

- The author proposes the *Implementer* (Codex, or Argon for an
  [eligible issue](argon-implementer.md#which-issues); Claude only when
  the maintainer names Claude for that issue); the maintainer confirms
  or changes it when adding `ready`.
- Always start from the matching template, for example
  `gh issue create --template Feature --title "…"`. The other templates
  are `Bug`, `Task` and `"Process improvement"`; with `--body-file`, copy
  the template's headings. Do not add, rename or drop headings.
- One issue fits one pull request of about 400 changed lines or fewer.
  Larger work is split into several issues before review.
- Put milestone issues in their GitHub milestone.
- No implementation starts on an issue without `ready`.

The lifecycle of a pull request is in [`pull-requests.md`](pull-requests.md).

## Keeping state

- **Status (#1):** read it only at the start of an orchestrating session;
  review, implementation and chore runs skip it. Orchestrating sessions also
  read `docs/roadmap.md` at the start. At the end of a unit of
  work, overwrite its body using its headings: under 30 lines and 3,000
  characters. *Recently done* holds only the latest unit; history lives in
  pull requests and issues. The `pr-chores` writer refuses a body outside
  these limits; shorten it before writing.
- **AI workflow log (#2):** when something goes wrong with the workflow —
  a tool hangs, a review misses something, work is redone, an instruction
  is misunderstood — add one comment: what happened · cost · likely cause
  · (optional) idea. No secrets or personal data.
- **Retrospective:** at the end of each milestone, or after about ten new
  log comments. An AI groups the comments since the last `Retrospective:`
  comment, proposes the smallest fixes (mechanical first, removal before
  addition), the maintainer picks, accepted ones become `process` issues,
  and a `Retrospective:` summary comment closes the round. Since #558 the
  initial adversarial review runs late, once: in step 4, running it on the
  draft and again after each merge of `main` cost 13 re-runs that found nothing. Since
  #664 it also follows Copilot's review, requested by hand on the draft,
  so a Copilot follow-up no longer forces a second run.
- **Roadmap:** `docs/roadmap.md`, updated when a milestone ends or the
  plan changes.

## Occasional audit

Argon may also do UI screenshot reviews and experiments.

Optional and maintainer-run, for example after large document changes; not
at every milestone. Antigravity looks for
contradictions between documents, configuration and code, with the prompt
in [`.github/prompts/audit.md`](../../.github/prompts/audit.md) (#116).

- Claude prepares a clean, detached checkout of a named commit and fills in
  the prompt from `origin/main`. It first checks that the commit is on
  `origin/main` (`git merge-base --is-ancestor`) and that
  `git ls-tree -r <commit>` lists no symlink (mode `120000`); a symlink
  could make a read leave the checkout.
- The maintainer runs `agy` interactively there. Allow a command once only
  if it matches one of the prompt's exact command forms (the two workspace
  checks and the `git --no-pager` reads), with no option the form does not
  list and no `-c` or environment setting; refuse everything else,
  including network access. Never choose "always allow".
- Claude checks every finding against the files, because the report can
  invent citations, and posts *valid* or *false positive* for each. Fixes
  go through a normal issue.

## Labels

| Label | Set by | Meaning |
|---|---|---|
| `ai-reviewed` | the orchestrating AI, after issue review approval or the [pull request review gates](pull-requests.md#lifecycle-of-a-pull-request) | The cross-review is done. A pull request is marked ready with it, and the maintainer can be asked. |
| `ready` | maintainer only | Issue approved; implementation may start. |
| `process` | template | Workflow improvement. |
| `bug` | template | Something is broken. |
