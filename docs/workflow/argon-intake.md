# Argon intake and scope

Assignment, steps, calibration and throughput follow the maintainer's decisions
(maintainer, 2026-10-23,
[#1561](https://github.com/tkakkie/ribbitto/issues/1561), decisions 1–4).

## Default assignment

An issue eligible under [Which issues](argon-implementer.md#which-issues)
names *Implementer* `Argon` unless the issue records why not.
[Issue review](reviewing.md#issue-review) checks this default. Eligibility
and the [boundary](argon-implementer.md#the-boundary) are unchanged.

## Steps and approval

1. **Now:** tests, documents and small validators.
2. Small behaviour fixes in tier B code where the issue supplies the failing test.
3. Larger tier B changes.

The maintainer approves each step at the time. Tier A and `high`-risk paths
stay out of Argon in every step.

**PROPOSAL — the maintainer may change this gate:** over the last ten counted
runs of the previous step, no merged defect found later, and average rework
at most one round per run. Rework counts rounds caused by correctness findings
from Claude, Codex or Copilot. Read the evidence from the [outcome record](#records).
The gate is evidence for the maintainer's decision, never a trigger for Claude
to advance on its own.

## Calibration

The second calibration round, with harder seeded defects in tier B areas as
[#1514](https://github.com/tkakkie/ribbitto/issues/1514) required, runs before
step 2. Its result is recorded before any step-2 issue is offered.

## Throughput

Implementer runs stay one at a time: one worker home, one lock. Claude prepares
the next issue's worktree and prompt while a run executes; reviews run in parallel
([#1533](https://github.com/tkakkie/ribbitto/issues/1533)). A second worker home
is outside this work and is not investigated for now.

## Records

Use the outcome format fixed in
[#1514](https://github.com/tkakkie/ribbitto/issues/1514): one comment per run on
[#1561](https://github.com/tkakkie/ribbitto/issues/1561) or its successor, with a
summary after every ten counted runs. Keep the existing
[per-PR records](argon-implementer.md#records).
