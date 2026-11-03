Read when: an issue's Done when defers a host measurement until after merge, or recording or judging that measurement.

# Deferred host measurements after merge

This order records the maintainer's [2026-11-01 decision on #1820](https://github.com/tkakkie/ribbitto/issues/1820#issuecomment-6551848391).
It applies only when the issue's *Done when*, approved by `ready`, defers the
measurement until after merge. It changes timing and record location only:
all [investigations conditions](investigations.md#host-measurements) apply,
including review, CI, host isolation, build/run routes, caps, approval,
cleanup and credential redaction. This document grants no waiver of any
review or CI requirement.

If the issue's *Done when* does not explicitly defer the measurement until
after merge (silence about timing is not deferral), the orchestrator checks
results against *Done when* before asking for merge and records them in the
PR. For deferred measurements, that check happens after merge as follows.

## Keep the issue open through merge

Use `Refs #N` as the first line of the PR body, replacing the template's
`Closes #` line for the issue whose measurement is deferred. A fix or revert
PR following an out-of-tolerance result also uses `Refs #N`, so its merge
cannot close the issue before re-measurement. No closing
keyword (`close`, `closes`, `closed`, `fix`, `fixes`, `fixed`, `resolve`,
`resolves`, `resolved`) naming that issue may appear in the PR title, body or
any commit message. The squash message is exactly the PR title plus body.

When asking for merge and again immediately before merging, the orchestrator
greps the current title, body and all commit messages for those keywords and
checks `gh pr view <N> --json closingIssuesReferences`: neither a closing
reference nor a closing link to the issue may remain. A Development-sidebar
link can close it too; `gh` has no unlink command, so remove it in the PR's
Development sidebar. If a link cannot be removed, or a keyword in a commit
message needs a history rewrite and a new head, the merge waits and the
orchestrator tells the maintainer. A rewritten head follows the normal
[push and review rules](README.md#lifecycle). After merge, the orchestrator
confirms the issue is still open. If GitHub closed it anyway, reopen it at
once and record that on the issue; reopening repairs closure and does not
replace prevention.

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
- The expected tree from
  `git merge-tree --write-tree <squash>^1 <reviewed-head>` (Git 2.38+).
  Check the exit status: 0 is clean, 1 means conflicts; on a conflict or any
  error, stop, compare no tree and request no measurement approval.
- On exit 0, the actual tree from `git rev-parse <squash>^{tree}`, both tree
  values and the comparison result. Compare the expected tree with the actual
  tree, not the reviewed head's tree directly with the squash tree: unrelated
  changes on `main` may have arrived since review. If equal, no extra review
  of the squash integration is needed; the existing PR reviews and the squash
  commit's own CI remain required. If different, the actual difference
  `git diff <expected-tree> <actual-tree>` needs an independent
  [follow-up check](review-followups.md#copilot-follow-up-check) under the
  existing review requirements. Anything substantive or not judgeable stops
  the procedure and is reported to the maintainer under the
  [substantive-delta rule](review-operations.md#third-ai-review).
- CI on the squash commit itself and its run URL. Here the required check is
  the `ci` job of the `ci.yml` push workflow run on that exact full SHA;
  the ruleset requires only `ci`. For example, use
  `gh api --method GET repos/tkakkie/ribbitto/actions/runs -f head_sha=<full-squash-SHA>`
  and verify the workflow, event, head SHA and successful `ci` job. Listing
  runs on `main` alone does not establish CI at this commit. `ci.yml` cancels
  in-progress runs on `main`. A cancelled or missing run is not success:
  locate the push run for that SHA and re-run it with `gh run rerun <run-id>`
  only when no newer run on `main` is in progress. If no run can be found,
  stop and report to the maintainer; request nothing until that exact SHA's
  push run has a successful `ci` job. A later push cannot produce a run at
  this SHA, and a successful run at another SHA never substitutes.

The issue record must include the reviewed head, squash commit, its first
parent, expected tree, actual tree, comparison result and CI run URL before
any run is requested for approval. This corrects the object of the comparison
and loosens no condition: no review or CI requirement may be skipped.
This evidence reconciles the squash SHA with investigations condition 2;
it does not replace any required review or check.

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

Edit that same entry at each step; each edit is a new comment-edit with the
status change dated. Issue comments are the authoritative state
and result record: the pending entry, approval, explicit start instruction and
result. Record chat approvals and start instructions there with their exact
scope. A result whose SHAs differ from the pending entry is invalid; establish
and approve the pair again before running it.

The result comment quotes the entry's base and head SHAs at the time of the
run and contains everything investigations condition 10 requires:
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
