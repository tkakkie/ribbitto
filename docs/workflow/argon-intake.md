# Argon intake and scope

Assignment, steps, calibration and throughput follow the maintainer's decisions
(maintainer, 2026-10-23,
[#1561](https://github.com/tkakkie/ribbitto/issues/1561),
[first set](https://github.com/tkakkie/ribbitto/issues/1561#issuecomment-6351468411),
[second set](https://github.com/tkakkie/ribbitto/issues/1561#issuecomment-6352203895)).

## Default assignment

An issue eligible under [Which issues](argon-implementer.md#which-issues)
names *Implementer* `Argon` unless the issue records why not.
This default applies only within maintainer-approved steps; today only step 1
is approved. Until its step is approved, a step-2 or step-3 issue names Argon
only when the maintainer says so.
Claude's [issue review](reviewing.md#issue-review) and the maintainer check
this default. The [trusted issue-review prompt](../../.github/prompts/issue-review.md)
is unchanged; changing reviewer prompts is a separate maintainer decision.
Eligibility and the [boundary](argon-implementer.md#the-boundary) are unchanged.

## Steps and approval

1. **Approved:** tests, documents, small validators, and normal-risk tier C
   ordinary feature implementation and small bug fixes that meet the existing
   [eligibility](argon-implementer.md#which-issues), to ease weekly Claude/Codex
   token limits.
2. **Unapproved:** small behaviour fixes in tier B code where the issue supplies
   the failing test.
3. **Unapproved:** larger tier B changes.

The maintainer approves each step at the time. Tier A and `high`-risk paths
stay out of Argon in every step. Organisation scoping, authorisation, sessions
and SSE delivery (the tier A areas) are excluded from every step, including
the tier B code in steps 2 and 3
([#1561, Out of scope](https://github.com/tkakkie/ribbitto/issues/1561)).

**PROPOSAL — the maintainer may change this gate:** over the last ten counted
runs of the previous step, no merged defect found later, and average rework
at most one round per run. Rework counts rounds caused by correctness findings
from Claude, Codex or Copilot. Read the evidence from the [outcome record](#records).
At a step change, also evaluate each pull request's quality and rework
individually from the per-run record.
The gate is evidence for the maintainer's decision, never a trigger for Claude
to advance on its own.

## Calibration

Before step 2, run the second calibration round once in tier B areas, with
harder seeded defects than [#1514](https://github.com/tkakkie/ribbitto/issues/1514):

- bytes versus characters;
- a missing `organization_id` scope together with the test that should have
  caught it;
- a wrong comparison in validation.

Record the results on [#1561](https://github.com/tkakkie/ribbitto/issues/1561)
before any step-2 issue is offered. This round also includes Argon as issue
reviewer on a few seeded issue specs.

## Throughput

Claude prepares the next issue's worktree and prompt while a run executes;
reviews run in parallel ([#1533](https://github.com/tkakkie/ribbitto/issues/1533)).
A second worker is investigated, designed and verified in parallel with #1561
in its own process issue [#1577](https://github.com/tkakkie/ribbitto/issues/1577):
separate `HOME`, worktree and lock per worker; guard, verify, preflight and
fail-closed stay unchanged. The first goal is two parallel implementer workers;
three or four may be considered later if needed. Production use of a second
worker needs the maintainer's separate approval. Until then, implementer runs
stay one at a time: one worker home, one lock. Detailed design belongs to #1577.

## Records

Use the outcome format fixed in
[#1514](https://github.com/tkakkie/ribbitto/issues/1514): one comment per run on
[#1561](https://github.com/tkakkie/ribbitto/issues/1561) or its successor, with a
summary after every ten counted runs. Keep the existing
[per-PR records](argon-implementer.md#records).
