# Adversarial review

**A pull request gets the adversarial review its tier asks for before the
maintainer is asked**, whatever its risk class: Grok (tier A), Muse Code
(tier B), or none (tier C); after a completed Grok report, most re-reviews
are Muse Code's (the completion rule). The required review must run; its
findings are advisory. It does not count towards the two review rounds. The
initial review runs after the cross-review, after merging the current
`main` (or confirming that no merge is needed) and after Copilot's one
review and any follow-up (*When it runs*, below), while the pull request is
still a draft, and before it is marked ready for review. Later pushes are
reviewed again under the completion rule.

## Which review runs

The tier follows what the change alters, **wherever the code lives**
(handlers, use cases, stores, queries, migrations, scripts), not its risk
class, which only decides how much the maintainer reads. Grok's
findings have come from concurrency, long-lived state and the gates
themselves: up to #497, 8 of its 74 reviews (#334, #427 and #431 in real
time and retention, #336 and #347 in launchers, #338, #340 and #364 in
gates), and none of step 3's org, identity and web refactors (#339, #498).
In #760–#839,
Grok's 23 first reviews found defects in 4 — #775 (read writes in posting's
transaction), #795 (browser retry state), #833 (an htmx swap of a live form),
#835 (a watch's ordering contract) — and its 7 re-runs none new (#840).
A tier follows what the change does; a past review without findings is
never a reason for a lower one.

**Precedence: A, then B, then C.** **When in doubt, the higher tier.** In
A's code areas Grok is for the changes A's first item lists; any other
change there, behaviour-preserving moves included, is tier B, by Muse Code
(a move still gets one review: #427's had a defect).

- **A — Grok required:**
  - in A's code areas — concurrency, caches or background work (goroutines,
    locks, timers, TTLs, clean-up and retention jobs), real-time delivery on
    the server (`internal/realtime/**`, the stream handlers and renderers in
    `internal/web`, the event log's queries and readers) or in the browser
    (`web/static`), and transactions whose order matters (the
    organisation's `event_seq` and the realtime append in posting,
    branching, setup and sign-up; read-state and cursor writes) — a change
    that adds, removes or reorders goroutines, locks, timers or background
    work; adds or changes long-lived in-memory state (registries,
    generations, caches, subscriptions) or the ordering contract its callers
    rely on; changes **which writes share a transaction or the order they
    run in**, or logic whose correctness rests on a lock or the
    transaction's isolation (decisions made under a lock); changes browser
    state handling (the SSE scripts, or htmx swaps, out-of-band ones
    included, that replace live forms or state, or what those forms carry
    across replacements); or changes
    the delivery contract: who is authorised for what (organisation and
    member), the organisation scope, which streams or members receive a
    frame, or where authorisation runs before a send;
  - any change to the rules that guard these areas and the AI and CI
    tooling: the AI launchers in `scripts/ai/**`; the trusted prompts and
    review instructions in `.github/prompts/**` and
    `.github/instructions/**`; `.github/workflows/**`; the files that define
    the review gates (`docs/workflow/README.md`, `reviewing.md`,
    `running-other-ai.md`, `running-grok.md`, this whole file, and
    `.github/pull_request_template.md`); and the security and scoping rules
    in `AGENTS.md` and `docs/domain/invariants.md`. Being documentation does
    not exempt a change from this item.
- **B — one review by Muse Code, run by its launcher** (Antigravity may add
  an extra review, which never satisfies a tier):
  - a change in A's code areas that keeps behaviour and order: moves,
    extractions, new seams and their closures, and tests (including new
    tests of races or of the event log);
  - any other behaviour change in A's code areas that matches none of A's
    first item: server stream wiring, renderers and ingress handlers that
    match none of those triggers;
  - **any** code change, behaviour-preserving moves included, in
    authentication, authorisation, sessions or organisation scoping: who is
    signed in, who is a member, what a member may see or do, and how a
    session expires (`internal/org/**`, `internal/identity/**`,
    `internal/web/middleware/**`, `internal/web/org.go`, the setup, sign-up
    and sign-in handlers, and their stores and queries);
  - a change that removes, skips or loosens a check `make check` or CI
    runs, wherever it lives: a test or an assertion, `Makefile`, `tools/**`,
    or a linter, vet or code-generation setting (`.golangci.yml`,
    `sqlc.yaml`).
  - a change wholly in isolated development tooling that A's first item
    would otherwise cover (*Isolated development tooling*, below).
- **C — none:** everything that matches no A or B case: tests outside A's
  and B's areas that weaken no check, comments and documentation outside
  the gate documents, lint rules that only tighten, dependency bumps, and
  ordinary feature work outside A and B.

**Isolated development tooling.** A change that would be tier A only by
A's first item (concurrency, caches or background work, real-time delivery,
ordered transactions) is tier B when it lies wholly in demonstrably isolated
development tooling. Such tooling is a command or script that runs only when
a developer invokes it: never in production, in deployment, in migrations or
in a CI job (its own tests in `make check` aside), and no production or CI
code imports or invokes it. A defect there skews only a developer's
measurement. Each
qualifying path is listed here with its evidence:

- `cmd/loadgen`, the load generator. It imports no other ribbitto package
  (`go list -deps ./cmd/loadgen`), and nothing imports it. No `Makefile`
  target, CI job, deployment file or migration runs it; `make check` runs
  only its own tests.

`scripts/deps.sh` is not listed: CI's `ci` job and `make check` run it.
The exception changes nothing else: production commands, migrations,
deployment tooling and shared code are classified by behaviour; code a tool
imports by its own area; A's second item applies to a tool's change too;
tests that guard A's areas stay tier B; and loosening a check `make check`
or CI runs stays at least tier B, in a listed tool too.

The template's *Adversarial* field names the reviews: `Grok`,
`Grok, then Muse Code re-review` (with the SHAs), `Muse Code (B)`,
`Muse Code (Grok unavailable until <date>)`, or
`skipped (C: <reason>)`, the reason naming the C case, for example
"dependency bump". "Behaviour unchanged" is never a C reason: in A's and B's
areas that is tier B. The other AI checks the tier against what the diff
does, not only the paths it touches.

**When it runs:** the initial review runs after the other AI approves and
its findings are fixed, after the branch has merged the current `main` or
it is confirmed that no merge is needed, and after Copilot, requested by
hand on the draft, has reviewed and any change answering it has passed the
Copilot follow-up check ([reviewing](reviewing.md)): on that head, while the
pull request is still a draft, immediately before `ai-reviewed`. GitHub
cannot merge a draft, so this holds the merge until the review completes.
It does not run alongside the first cross-review. A report on an earlier
head covers a later one only under the completion rule below.

**A required review counts only when it completes:** a report on the pull
request at its head commit, each finding with a disposition. After a later
push (a fix for a finding, a Copilot follow-up or a merge of `main`
included) whose diff from the last adversarially reviewed head (or the
base, if none) matches tier A or B, the **pull request's** tier is
recomputed over its whole diff at the new head, never lower than that
diff's, and reviewed again:

- a tier C pull request lifted to B or A gets its first review, and a tier
  A one without a completed Grok report gets Grok;
- a tier A pull request with a completed Grok report gets **Muse Code**
  when every commit since the **last Grok-reviewed head** only fixes
  reported findings (after the adversarial follow-up check in
  [reviewing](reviewing.md)), answers Copilot or the cross-review, or
  merges `main` (what `main` brings was reviewed in its own pull request;
  a conflict resolution beyond combining both sides counts as this pull
  request's change); it gets **Grok again** when those commits — counted from
  the last Grok review, however many Muse Code re-reviews came between —
  add anything of A's first item beyond what a fix needs, or the
  maintainer asks for Grok. When in doubt, Grok;
- a tier B pull request gets Muse Code again.

A pull request already marked ready is converted back to a draft until that
review completes. An earlier report
covers a later head only when every later commit is tier C. A time-out, an outage or an
empty run is not a review: retry once, then ask the maintainer.
When Grok is unavailable (quota or outage), the failed runs and their
errors are recorded on the pull request, a tier A pull request gets Muse
Code under the same rule, and the maintainer decides whether to merge or
wait for Grok.

## Running Grok

Run it from the maintainer's checkout, taking the launcher from `main`:

```sh
git fetch origin main &&
  launcher=$(git show origin/main:scripts/ai/grok-review.sh) &&
  bash -c "$launcher" grok-review <pr-number>
```

Never run a pull request's copy of `scripts/ai/grok-review.sh`. The
launcher, its security boundary and its limits are in
[`running-grok.md`](running-grok.md).

## Muse Code

Muse Code (#330, from the trial in #246) is tier B's reviewer, tier A's
re-reviewer after a completed Grok report (#840) and tier A's fallback when
Grok is unavailable, under the completion rule above. It may
also review any other pull request as an optional, extra adversarial
reviewer; an extra run never satisfies or waives a requirement (the
required tier's review, the Claude ↔ Codex rounds, Copilot's follow-up, the
maintainer's decisions).

- It reviews the same head commit with the same context Grok gets, run as in
  [*Running Muse Code*](running-other-ai.md#running-muse-code).
- Claude posts the report as `**Adversarial review — Muse Code** (at <SHA>)`,
  or for a tier A re-review
  `**Adversarial review — Muse Code** (re-review after Grok at <SHA>, at <SHA>)`.
  A valid finding gets a disposition like any other: fixed, a follow-up
  issue, or declined with the reason.
- If an **extra** run fails (a provider outage, the time limit), record that
  in the pull request and carry on. A failed **required** run is retried
  once, then the maintainer decides.
