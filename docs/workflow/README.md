# AI development workflow

How work moves from an idea to `main` in ribbitto. Written for the AI tools
that do most of the work and for the maintainer who decides. `AGENTS.md`
covers how to write code; these files cover how work flows. If the two ever
disagree, fix the disagreement in a pull request.

## Index

| File | Covers |
|---|---|
| `README.md` (this file) | Principles, roles, the lifecycle of an issue, keeping state, the occasional audit, labels |
| [`pull-requests.md`](pull-requests.md) | The lifecycle of a pull request, from a `ready` issue to the merge decision |
| [`reviewing.md`](reviewing.md) | Reviewing, closure verification, the adversarial follow-up check, risk |
| [`copilot.md`](copilot.md) | The Copilot follow-up check, and docs-only pull requests, which skip Copilot |
| [`adversarial-review.md`](adversarial-review.md) | Which changes need an Argon adversarial review (Codex for Argon's own pull requests), and when it completes |
| [`running-argon.md`](running-argon.md) | running Argon's issue and adversarial reviews: setup, the Go launcher, its boundary and evidence |
| [`running-other-ai.md`](running-other-ai.md) | Running Codex or Claude headless, how the maintainer's sessions run Codex, and the handoff of another AI's code |
| [`argon-implementer.md`](argon-implementer.md) | Argon implementing eligible normal-risk tier B and C issues: eligibility, roles, boundary, preflight, records |
| [`argon-pull-requests.md`](argon-pull-requests.md) | Argon's pull requests: who reviews them, Codex's fixed adversarial review invocation, mutation evidence on CI |

## Principles

1. **Every issue and every pull request an AI writes is reviewed by an AI
   that did not write it before the maintainer is asked to look at it** —
   including any documentation the pull request changes. Review reports,
   Status updates, workflow-log comments and chat messages are not
   reviewed.
2. **The maintainer decides at two normal delivery gates:** approving an
   issue (`ready`) and merging a pull request. Everything between is done
   by the AIs. Deciding is the gate, not clicking: after the maintainer's
   own explicit instruction in the chat for a specific pull request, an AI
   may execute the merge (see
   [Lifecycle of a pull request](pull-requests.md#lifecycle-of-a-pull-request)).
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

| Who | Does |
|---|---|
| Maintainer | Writes rough ideas, approves issues, decides every merge. |
| Claude | Writes and details issues, orchestrates (drives the other CLIs), reviews Codex's issues and pull requests and Argon's pull requests, and does the [CI-only handoff](running-other-ai.md#handoff-of-another-ais-code). |
| Codex | Writes issues, implements tier A, `high` risk and other work Argon may not take (except issues for which the maintainer names Claude), reviews Claude's other work and issues when Argon's run does not count, and runs the adversarial review of Argon's tier B pull requests ([Argon's pull requests](argon-pull-requests.md)). |
| Copilot | Reviews each pull request **once** (not a [docs-only](copilot.md#docs-only-pull-requests) one), at **Lite**, guided by `.github/instructions/code-review.instructions.md`. The orchestrating AI requests that review by hand on the draft (`gh pr edit <n> --add-reviewer @copilot`, verified on #681), after the cross-review approves and `main` is merged, and before the initial adversarial review. The `main` ruleset does not request it on ready, and drafts and new pushes do not trigger it; re-request it by hand if a later change needs another look. Advisory. Balanced is not used: it can only be chosen by hand in the *Reviewers* panel, and the CLI and API cannot set the effort. |
| Antigravity / Argon | Reviews Claude-written issues through the trusted `argonreview issue` subcommand, with Codex fallback if the run does not count (two rounds total across both; [reviewing](reviewing.md)). One required adversarial review for tier A and tier B pull requests, through `cmd/argonreview` ([running it](running-argon.md)), except its own: Codex reviews Argon's tier B pull requests ([Argon's pull requests](argon-pull-requests.md)). Findings are advisory. Implements eligible normal-risk tier B and C issues, reviewed by Claude ([as implementer](argon-implementer.md)). Optional: the occasional documentation audit (below), UI screenshot review and experiments. |

Argon implements eligible normal-risk tier B and C issues under its
[standing rules](argon-implementer.md); Codex implements everything else,
except an issue for which the maintainer names Claude.
Claude implements only when the maintainer names Claude for a specific
issue (maintainer, 2026-10-17,
[#1252](https://github.com/tkakkie/ribbitto/issues/1252)). Existing `ready`
issues naming Claude are reassigned when started, without a bulk edit.

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
  and a `Retrospective:` summary comment closes the round. Since #558 the
  initial adversarial review runs late, once: in step 4, running it on the
  draft and again after each merge of `main` cost 13 re-runs that found nothing. Since
  #664 it also follows Copilot's review, requested by hand on the draft,
  so a Copilot follow-up no longer forces a second run.
- **Roadmap:** `docs/roadmap.md`, updated when a milestone ends or the
  plan changes.

## Occasional audit

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
| `ai-reviewed` | the orchestrating AI, after the reviewer approves (for a pull request, after Copilot's review, if requested, is answered and the initial adversarial review its tier requires has completed; after a fix for its finding, also the follow-up check and the re-review the completion rule names) | The cross-review is done. A pull request is marked ready with it, and the maintainer can be asked. |
| `ready` | maintainer only | Issue approved; implementation may start. |
| `process` | template | Workflow improvement. |
| `bug` | template | Something is broken. |
