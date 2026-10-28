# Argon intake and scope

Assignment, steps, calibration and throughput follow the maintainer's decisions
(maintainer: 2026-10-23,
[#1561](https://github.com/tkakkie/ribbitto/issues/1561),
[first set](https://github.com/tkakkie/ribbitto/issues/1561#issuecomment-6351468411),
[second set](https://github.com/tkakkie/ribbitto/issues/1561#issuecomment-6352203895),
[third set](https://github.com/tkakkie/ribbitto/issues/1561#issuecomment-6354263122);
[step 2 approval, 2026-10-24](https://github.com/tkakkie/ribbitto/issues/1561#issuecomment-6380944798);
[step 3 approval, 2026-10-28](https://github.com/tkakkie/ribbitto/issues/1561#issuecomment-6447736760)).

## Default assignment

An issue eligible under [Which issues](argon-implementer.md#which-issues)
names *Implementer* `Argon` unless the issue records why not.
This default applies only within maintainer-approved steps; today steps 1, 2 and 3
are approved.
Claude's [issue review](reviewing.md#issue-review) and the maintainer check
this default. The [trusted issue-review prompt](../../.github/prompts/issue-review.md)
is unchanged; changing reviewer prompts is a separate maintainer decision.
Eligibility and the [boundary](argon-implementer.md#the-boundary) are unchanged.

## Steps and approval

1. **Approved:** tests, documents, small validators, normal-risk
   [tier C](adversarial-review.md#which-review-runs) ordinary feature
   implementation (ordinary feature work outside tiers A and B) and tier C small
   bug fixes (a bug fix that is tier B is step 2), to ease weekly
   Claude/Codex token limits. Every step-1 item also meets the existing
   [eligibility](argon-implementer.md#which-issues), including that it can be
   verified without running its code locally: no `make dev` or browser check
   unless the maintainer runs it, so a tier C feature that needs one is eligible
   only when the maintainer himself performs that verification.
2. **Approved** (maintainer, 2026-10-24,
   [#1561](https://github.com/tkakkie/ribbitto/issues/1561#issuecomment-6380944798)):
   small behaviour fixes in tier B code where the issue supplies the failing test.
3. **Approved** (maintainer, 2026-10-28,
   [#1561](https://github.com/tkakkie/ribbitto/issues/1561#issuecomment-6447736760)):
   larger tier B changes.

Step 2 was approved before the step-1 ten-run summary and per-run quality and
rework evaluation were posted on #1561; the [ten-run summary](https://github.com/tkakkie/ribbitto/issues/1561#issuecomment-6381749111)
and [per-PR evaluation](https://github.com/tkakkie/ribbitto/issues/1561#issuecomment-6381749378)
were both posted on 2026-10-24 as a record, not a gate for the approved step.

The maintainer approves each step at the time. Tier A and `high`-risk paths
stay out of Argon in every step. Production code changes to organisation
scoping, authorisation, sessions and SSE delivery stay out of Argon in every
step, including the tier B code in steps 2 and 3
([#1561, Out of scope](https://github.com/tkakkie/ribbitto/issues/1561)), while
tests that add coverage in those areas without changing behaviour are allowed
under the existing eligibility and tier rules, are tier B
([adversarial-review.md](adversarial-review.md#which-review-runs)) and belong
to step 1.

**PROPOSAL — the maintainer may change this gate:** over the last ten counted
runs of the previous step, no merged defect found later, and average rework
at most one round per run. Rework counts rounds caused by correctness findings
from Claude, Codex or Copilot. Read the evidence from the [outcome record](#records).
At a step change, also evaluate each pull request's quality and rework
individually from the per-run record.
The gate is evidence for the maintainer's decision, never a trigger for Claude
to advance on its own.

## Calibration

Before step 2, the second calibration round ran once in tier B areas, with
harder seeded defects than [#1514](https://github.com/tkakkie/ribbitto/issues/1514),
as recorded on #1561:

- a cut counting bytes instead of code points;
- a missing channel guard (an `organization_id` omission could not be seeded
  because `tools/scopecheck` rejects SQL without the predicate, and Go callers
  take the organisation from the membership);
- a wrong range bound in validation (one past the end);
- a find-or-create race;
- an error type that breaks `errors.Is`;
- a control: a pure refactor.

The results were recorded on
[#1561](https://github.com/tkakkie/ribbitto/issues/1561#issuecomment-6380849083)
before any step-2 issue was offered. This round also included Argon as issue
reviewer on a few seeded issue specs.

## Throughput

Claude prepares the next issue's worktree and prompt while a run executes;
reviews run in parallel ([#1533](https://github.com/tkakkie/ribbitto/issues/1533)).
Two Argon implementer workers run in production under
[#1577](https://github.com/tkakkie/ribbitto/issues/1577): separate `HOME`,
worktree and lock per worker; guard, verify, preflight and fail-closed stay
unchanged. Up to two implementer runs may execute at once; a third worker
needs the maintainer's approval. See the [design](argon-workers.md) and
[operation](argon-workers-operation.md).

## Records

Use the outcome format fixed in
[#1514](https://github.com/tkakkie/ribbitto/issues/1514): one comment per run on
[#1561](https://github.com/tkakkie/ribbitto/issues/1561) or its successor, with a
summary after every ten counted runs. Keep the existing
[per-PR records](argon-implementer.md#records).
