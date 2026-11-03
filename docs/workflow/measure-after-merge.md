Read when: an issue's Done when defers a host measurement until after merge, or recording or judging that measurement.

# Deferred host measurements after merge

This order records the maintainer's [2026-11-01 decision on #1820](https://github.com/tkakkie/ribbitto/issues/1820#issuecomment-6551848391).
It applies only when the issue's *Done when*, approved by `ready`, defers the
measurement until after merge. It changes timing and record location only:
all [investigations conditions](investigations.md#host-measurements) apply,
including review, CI, host isolation, build/run routes, caps, approval,
cleanup and credential redaction. No review or CI requirement can be skipped,
including at the maintainer's individual judgement.

For measurements that gate merge, the orchestrator checks results against the
issue's *Done when* before asking for merge and records them in the PR. For
deferred measurements, that check happens after merge as follows.

## Keep the issue open through merge

Use `Refs #N` as the first line of the PR body, replacing the template's
`Closes #` line for the issue whose measurement is deferred. No closing
keyword (`close`, `closes`, `closed`, `fix`, `fixes`, `fixed`, `resolve`,
`resolves`, `resolved`) naming that issue may appear in the PR title, body or
any commit message: GitHub builds the squash message from them.

Before asking for merge, the orchestrator greps all three for those keywords
and removes any closing reference to that issue. Also check
`gh pr view <N> --json closingIssuesReferences`: the issue must not appear.
Unlink it if it does; a Development-sidebar link can close it too. Check the
final squash message for closing references as well. After merge, confirm the
issue is still open. If GitHub closed it anyway, reopen it at once and record
that on the issue; reopening repairs closure and does not replace prevention.

Review and CI still complete at the PR head before `ai-reviewed`, as in
[investigations condition 1](investigations.md#host-measurements) and the
[PR lifecycle](README.md#lifecycle). Merge still needs the maintainer's
explicit instruction for that PR and head.

## Establish the exact base and head before requesting approval

Post all of the following in the issue, with full SHAs, before requesting
approval for any run:

- The PR head that last had every required review and green CI.
- The squash commit (measurement head) and its first parent (measurement
  base); show the first-parent relationship with `git rev-parse <squash>^1`.
- Both tree values from `git rev-parse <reviewed-head>^{tree}` and
  `git rev-parse <squash>^{tree}`, and whether they match.
- CI on the squash commit itself: workflow runs whose head SHA is that full
  squash SHA, every required check successful, and their run URLs. For example,
  use `gh api --method GET repos/tkakkie/ribbitto/actions/runs -f head_sha=<full-squash-SHA>`
  and verify the returned head SHAs and checks. Listing runs on `main` alone
  does not establish CI at this commit.

If the trees differ, the difference must get an independent
[follow-up check](review-followups.md#copilot-follow-up-check) of
`<reviewed-head>..<squash>` under the existing review requirements, and CI
must pass at the squash commit, before requesting approval. Neither review
nor CI may be waived. This evidence reconciles the squash SHA with
investigations condition 2; it does not replace any required review or check.

The maintainer must approve the exact two full build/run SHAs, the method and
the cap, and give an explicit start instruction. A measurement is never
self-scheduled. Build and run both approved SHAs through the unchanged routes
and isolation in [investigations](investigations.md); every other condition,
including condition 2's per-push and setup-validation approval, remains.

## Keep one issue record and a PR link

At merge, the orchestrator posts one pending entry as a comment on the issue:

| issue | PR | base | head | status |
|---|---|---|---|---|
| #N | #PR | full first-parent SHA | full squash SHA | pending approval |

Edit that same entry at each step. Issue comments are the authoritative state
and result record: the pending entry, approval, explicit start instruction and
result. Record chat approvals and start instructions there with their exact
scope. A result whose SHAs differ from the pending entry is invalid; establish
and approve the pair again before running it.

The result comment contains everything investigations condition 10 requires:
host measurement, SHAs, conditions (toolchain, image digest, restrictions),
exact commands, results and the generation diff listing, with credentials
redacted. The orchestrator comments on the merged PR with a link to that
result comment. For these runs, this link satisfies condition 10's PR record;
do not duplicate the result. Merge-gating measurements keep their PR record.

## Judge the result before closing

The orchestrator checks the result against the issue's *Done when* and records
the judgement on the issue. A result comment alone never closes the issue.
If a tolerance is exceeded or a run fails its own conditions, keep the issue
open until a fix is merged and re-measured within tolerance, a revert is
merged, or the maintainer approves an exception in chat. Record the exception's
condition, magnitude, reason, issue and SHAs, and where it is documented.
An exception to a measurement result waives no review or CI requirement.

A problem found in a measurement becomes its own issue, as decided on
2026-11-02 and recorded in [#2088](https://github.com/tkakkie/ribbitto/issues/2088)
for #1825. Link that issue from the measured issue; the measured issue closes
only after the problem issue is resolved or the exception is recorded.
