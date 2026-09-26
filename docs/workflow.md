# AI development workflow

How work moves from an idea to `main` in ribbitto. Written for the AI tools
that do most of the work and for the maintainer who decides. `AGENTS.md`
covers how to write code; this file covers how work flows. If the two ever
disagree, fix the disagreement in a pull request.

## Principles

1. **Every issue and every pull request an AI writes is reviewed by the
   other AI before the maintainer is asked to look at it** — including any
   documentation the pull request changes. Review reports, Status updates,
   workflow-log comments and chat messages are not reviewed.
2. **The maintainer decides at two normal delivery gates:** approving an
   issue (`ready`) and merging a pull request. Everything between is done
   by the AIs. The maintainer is also asked when a review does not
   converge (see "Reviewing") and when choosing retrospective proposals.
3. **Process grows only from evidence.** From now on, a new process rule
   comes from a `process` issue whose *Problem* section links the AI
   workflow log comments (#2) that motivated it; a rule that never
   prevented anything can be removed.

## Roles

| Who | Does |
|---|---|
| Maintainer | Writes rough ideas, approves issues, merges pull requests. |
| Claude | Writes issues, implements (mainly design-heavy work), reviews Codex's work, drives the other CLIs. |
| Codex | Writes issues, implements (mainly well-specified work), reviews Claude's work. |
| Copilot | Reviews every pull request automatically (drafts included) at **Lite**, the repository setting, guided by `.github/instructions/code-review.instructions.md`. Advisory. Balanced is not used: it can only be chosen by hand in the *Reviewers* panel, and the CLI and API cannot set the effort. |
| Grok | Adversarial review of `high` pull requests (from M1), run with `scripts/ai/grok-review.sh`. Advisory. |
| Antigravity | Optional: UI screenshot review, experiments, stand-in for Grok. |

Claude and Codex should end up with roughly equal shares of implementation.
If one has done the last few issues, give the next one to the other.

## Lifecycle of an issue

```
idea (maintainer, one line) or finding (AI)
  → author AI writes the issue from a template in .github/ISSUE_TEMPLATE/
  → the AI that did not write the issue reviews it  (see "Reviewing")
  → author AI applies the review
  → reviewer approves → add label `ai-reviewed`
  → maintainer reads it → adds `ready`, or comments  ← gate 1
```

- The author proposes the *Implementer* (Claude or Codex, keeping the
  shares even); the maintainer confirms or changes it when adding `ready`.
- Always start from the matching template, for example
  `gh issue create --template Feature --title "…"`. The other templates
  are `Bug`, `Task` and `"Process improvement"`; with `--body-file`, copy
  the template's headings. Do not add, rename or drop headings.
- One issue fits one pull request of about 400 changed lines or fewer.
  Larger work is split into several issues before review.
- Put milestone issues in their GitHub milestone.
- No implementation starts on an issue without `ready`.

## Lifecycle of a pull request

```
`ready` issue
  → implementer AI works in its own worktree
    (maintainer setup: `../wt/<name>` next to the main checkout;
     branch claude/<topic> or codex/<topic>)
  → draft pull request from the template, linked with "Closes #N"
  → CI and Copilot run automatically
  → the AI that did not implement it reviews        (see "Reviewing")
  → implementer applies the review (and Copilot comments it agrees with)
  → at most two rounds; if round 2 leaves only mechanical fixes,
    closure verification; otherwise the maintainer decides
  → reviewer approves → label `ai-reviewed` → mark ready for review
  → Claude explains the PR to the maintainer in Japanese, in the chat
    (the PR itself stays in English)
  → maintainer reviews and merges                  ← gate 2
```

- `high` risk: the maintainer reads the whole diff; from M1, Grok also
  runs an adversarial review before the maintainer is asked.
- `normal` risk: the maintainer reads the summary and "Look here".
- All merges are manual. Auto-merge will be designed in its own `process`
  issue if manual merging ever becomes a burden.

## Reviewing

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

4. All pass → `ai-reviewed` and the normal merge decision. Any other change
   in the diff, a failed check, a new blocking finding or anything needing
   a decision → ask the maintainer.

A later merge of `main` to resolve conflicts is reported in a PR comment
listing the files and how they were resolved; if the resolution does more
than combine both sides, it needs a normal review.

## Adversarial review

From M1 on, **every `high` pull request gets one adversarial review by Grok
before the maintainer is asked.** Running it is mandatory; its findings are
advisory. It does not count towards the two review rounds.

Run it from the maintainer's checkout, taking the launcher as it is on
`main` — never a copy that a pull request could have changed:

```sh
git fetch origin main && bash <(git show origin/main:scripts/ai/grok-review.sh) <pr-number>
```

- **The invocation above is the security boundary.** If the launcher is
  run as a file that differs from `origin/main:scripts/ai/grok-review.sh`,
  it refuses to run — but that only catches an accidentally edited copy: a
  malicious copy runs its own code before any check (a script cannot vouch
  for itself). Never run `scripts/ai/grok-review.sh` from a PR checkout.
- It checks out the PR head in a temporary worktree and builds the prompt
  from `.github/prompts/adversarial.md` **on `origin/main`** (a PR cannot
  rewrite its own review instructions). Everything the PR controls — title,
  description and diff — is appended as **one JSON object** on the last
  line, so escaping keeps it from ending early or adding instructions; the
  prompt also tells Grok that every file in the worktree is untrusted data.
- Grok runs read-only: plan mode **and** only the `read_file`, `list_dir`
  and `grep` tools, no web search, stdin closed.
- Those tools are not a filesystem sandbox, so a PR whose tree contains any
  symlink (mode `120000`) is refused before anything is checked out: a
  symlink could otherwise let Grok read files outside the worktree.
- It stops Grok and everything Grok started after `RIBBITTO_GROK_TIMEOUT`
  seconds (1–86400, default 1200; exit 124), and removes the worktree and
  temporary files on success, failure, timeout, Ctrl-C (130) and `TERM`
  (143), reporting any cleanup failure. When Grok itself fails, the script
  exits with Grok's status.
- The title, description, base and head come from one `gh pr view`; the
  diff is computed locally from that base and head, so everything Grok sees
  describes the same commit even if the PR is pushed in the meantime.
- `RIBBITTO_GROK_MODEL` picks a model (`grok models` lists them).
  `RIBBITTO_GROK_TRUSTED_REF` changes where the launcher and prompt must
  come from; use it only to test a PR that edits them.
- Claude posts the report as a PR comment headed
  `**Adversarial review — Grok** (at <SHA>)` and adds, for each finding,
  *valid* (fixed in the PR or tracked as an issue) or *false positive* with
  the reason.

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

# Pull request (in a clean worktree at the PR head, after git fetch).
# Not `codex exec review --base …`: it rejects a custom prompt.
limit 1800 codex exec -s read-only -o review.md \
  "Review this pull request: run 'git diff origin/main...HEAD'. Follow docs/workflow.md#reviewing and reply in the report format." < /dev/null
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
