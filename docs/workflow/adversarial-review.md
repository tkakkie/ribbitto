# Adversarial review

**A pull request gets the adversarial review its tier asks for before the
maintainer is asked**, whatever its risk class: one Argon review for tier A
or B, or none for tier C. The required review must run; its
findings are advisory. It does not count towards the two review rounds. The
initial review runs after the cross-review, after merging the current
`main` (or confirming that no merge is needed) and after Copilot's one
review and any follow-up (*When it runs*, below), while the pull request is
still a draft, and before it is marked ready for review. Later pushes are
reviewed again under the completion rule.

## Which review runs

The tier follows what the change alters, **wherever the code lives**
(handlers, use cases, stores, queries, migrations, scripts), not its risk
class, which only decides how much the maintainer reads.

**Historical evidence that led to the tiers.** Grok's
findings came from concurrency, long-lived state and the gates
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
A's code areas a change to what the code does or its order is tier A,
except B's narrow carve-out; a change there that keeps both is tier B
(a move still gets one review: #427's had a defect).

- **A — one Argon review required:**
  - in A's code areas — concurrency, caches or background work (goroutines,
    locks, timers, TTLs, clean-up and retention jobs), real-time delivery on
    the server (`internal/realtime/**`, the stream handlers and renderers in
    `internal/web`, the event log's queries and readers) or in the browser
    (`web/static`), and transactions whose order matters (the
    organisation's `event_seq` and the realtime append in posting,
    branching, setup and sign-up; read-state and cursor writes) — any change
    to behaviour or order outside B's carve-out, including: goroutines,
    locks, timers and background work; long-lived in-memory state and its
    ordering contract; which writes share a transaction, their order, and
    logic resting on a lock or the transaction's isolation; the stream
    loop's gaps, cursor and event-log reads; what a page's `sse-connect`
    asks for (cursor, interests, channel, topic, `presence-after`);
    retention's deletion; what a
    `web/static` script sends, when and with which cursor, its retries and
    visibility state; htmx or SSE swaps of live forms or state (`sse-swap`,
    `hx-*`), in scripts or templates; output kept in
    a cache shared across members; and the delivery contract — who is
    authorised for what (organisation and member), the organisation scope,
    which streams or members receive a frame, and authorisation before each
    send;
  - any change to the rules that guard these areas and the AI and CI
    tooling: the Argon launcher in `cmd/argonreview/`; the trusted prompts and
    review instructions in `.github/prompts/**` and
    `.github/instructions/**`; `.github/workflows/**`; the files that define
    the review gates (`docs/workflow/README.md`, `reviewing.md`,
    `running-other-ai.md`, `running-argon.md`, `argon-implementer.md`, this
    whole file, and `.github/pull_request_template.md`); and the security and scoping rules
    in `AGENTS.md` and `docs/domain/invariants.md`. Being documentation does
    not exempt a change from this item.
- **B — one Argon review required:**
  - a change in A's code areas that keeps behaviour and order: moves,
    extractions, new seams and their closures, and tests (including new
    tests of races or of the event log);
  - the carve-out: a behaviour change confined to server stream wiring,
    renderers or ingress handlers that touches nothing A's first item
    names (a renderer whose output a shared cache keeps may vary only by
    that cache's key);
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

- `cmd/loadgen`, the load generator: it imports no other ribbitto package
  (`go list -deps`), nothing imports it, and only `make check` runs it, for
  its own tests.

`tools/deps` is not listed: CI and `make check` run it.
The exception changes nothing else: production commands, migrations,
deployment tooling and shared code are classified by behaviour; code a tool
imports by its own area; A's second item applies to a tool's change too;
tests that guard A's areas stay tier B; and loosening a check `make check`
or CI runs stays at least tier B, in a listed tool too.

The template's *Adversarial* field names the review: `Argon (A|B) at <SHA>`
or `skipped (C: <reason>)`, the reason naming the C case, for example
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
recomputed over its whole diff at the new head and never lowered. Argon
reviews again. A tier C pull request lifted to B or A gets its first review,
compared against the base when no reviewed head exists; one Argon implemented
leaves its trial instead, and the maintainer decides, since Argon never
reviews its own work ([trial](argon-implementer.md#which-issues)). Fixes first get the
follow-up check in [reviewing](reviewing.md).

A pull request already marked ready is converted back to a draft until that
review completes. An earlier report covers a later head only when every
later commit is tier C. A failed, timed-out or empty run never counts as a
review; record these runs and their failures on the pull request. A
time-out, an outage, exhausted quota or an empty run is retried once, then
the maintainer decides.

## Running Argon

Use `cmd/argonreview` through the trusted Go invocation in
[`running-argon.md`](running-argon.md), which documents its setup, security
boundary and limits. Build from clean `origin/main` as documented there;
never build or run a pull request's copy of the launcher.

Post the report as `**Adversarial review — Gemini 4 Argon (tier A|B)** (at <SHA>)`.
Each finding gets a disposition: fixed, a follow-up issue, or declined with
the reason.
