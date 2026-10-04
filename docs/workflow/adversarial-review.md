# Adversarial review

**A pull request gets the adversarial review its tier asks for before the
maintainer is asked**, whatever its risk class: Grok (tier A), Muse Code
(tier B), or none (tier C). The required review must run; its
findings are advisory. It does not count towards the two review rounds. It
runs after the cross-review, while the pull request is still a draft, and
before it is marked ready for review.

## Which review runs

The tier follows what the change alters, **wherever the code lives**
(handlers, use cases, stores, `db/queries/**`, `db/migrations/**`, scripts),
not its risk class, which only decides how much the maintainer reads. Grok's
findings have come from concurrency, long-lived state and the gates
themselves (#339): of 74 Grok reviews in the 100 pull requests up to #497,
the 8 with findings were #334, #427 and #431 (real time and retention),
#336 and #347 (launchers), and #338, #340 and #364 (gates), while step 3's
about 30 org, identity and web refactors had none (#498).

**Precedence: A, then B, then C.** **When in doubt, the higher tier.** In
the code areas of A (concurrency, real-time delivery, ordered transactions),
Grok is for changes to **what the code does or the order it does it in**; a
change there that keeps both is tier B, once, by Muse Code (#427 moved
retention into realtime's store and Grok found a defect, so such moves still
get one adversarial review).

- **A — Grok required:**
  - in concurrency, caches or background work (goroutines, locks, TTLs,
    clean-up and retention jobs), in real-time delivery on the server
    (`internal/realtime/**`, the stream handlers and renderers in
    `internal/web`, the event log's queries and readers) or in the browser
    (the SSE scripts in `web/static`), or in transactions whose order
    matters (the organisation's `event_seq` and the realtime append in
    posting, branching, setup and sign-up): a change that **alters
    behaviour**, adds, removes or reorders goroutines, locks or background
    work, or changes **which writes share a transaction or the order they
    run in**;
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
- **C — none:** everything that matches no A or B case: tests outside A's
  and B's areas that weaken no check, comments and documentation outside
  the gate documents, lint rules that only tighten, dependency bumps, and
  ordinary feature work outside A and B.

The template's *Adversarial* field names the review: `Grok`,
`Muse Code (B)`,
`Muse Code (Grok unavailable until <date>)`, or
`skipped (C: <reason>)`, the reason naming the C case, for example
"dependency bump". "Behaviour unchanged" is never a C reason: in A's and B's
areas that is tier B. The other AI checks the tier against what the diff
does, not only the paths it touches.

**A required review counts only when it completes:** a report on the pull
request at its head commit, each finding with a disposition. After a later
push (a Copilot follow-up or a merge of `main` included) whose diff matches
tier A or B, the **pull request's** tier, not the push's, is reviewed again
at the new head: a tier A pull request needs Grok again. An earlier report
covers a later head only when every later commit is tier C. A time-out, an outage or an
empty run is not a review: retry once, then ask the maintainer.
When Grok is unavailable (quota or outage), a tier A pull request gets Muse
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

Muse Code (#330, from the trial in #246) is tier B's reviewer and tier A's
fallback when Grok is unavailable, under the completion rule above. It may
also review any other pull request as an optional, extra adversarial
reviewer; an extra run never satisfies or waives a requirement (the
required tier's review, the Claude ↔ Codex rounds, Copilot's follow-up, the
maintainer's decisions).

- It reviews the same head commit with the same context Grok gets, run as in
  [*Running Muse Code*](running-other-ai.md#running-muse-code).
- Claude posts the report as `**Adversarial review — Muse Code** (at <SHA>)`.
  A valid finding gets a disposition like any other: fixed, a follow-up
  issue, or declined with the reason.
- If an **extra** run fails (a provider outage, the time limit), record that
  in the pull request and carry on. A failed **required** run is retried
  once, then the maintainer decides.
