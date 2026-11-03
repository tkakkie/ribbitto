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

Use `Refs #<measured issue>` as the first line of the PR body, replacing the
template's `Closes #` line for the issue whose measurement is deferred.
Any PR that fixes, reverts or otherwise changes what that deferred measurement
measures before the measured issue is judged complete also uses
`Refs #<measured issue>`. This includes measurement-condition violations,
defects found before a run, and reverts, as well as out-of-tolerance results.
It may still `Closes #<problem issue>` for a separate problem issue.
`<measured issue>` is the issue awaiting measurement judgement; the delivery
brief's `<N>` is the implementer's own issue, which may be that separate
problem issue.

No closing keyword naming the measured issue may appear in the PR title, body
or any commit message. The squash message is exactly the PR title plus body.
Use `grep -niE` with this pattern on all three texts; check every reference
on each matched line against the measured issue, including lists of issues
(unqualified `#N` means this repository). It covers any letter case, optional colons, qualified
references and issue URLs:

```text
(^|[^[:alnum:]_])(close[sd]?|fix(es|ed)?|resolve[sd]?)[[:space:]]*:?[[:space:]]*(#|[[:alnum:]_.-]+/[[:alnum:]_.-]+#|https://github\.com/[[:alnum:]_.-]+/[[:alnum:]_.-]+/issues/)[0-9]+([^[:digit:]]|$)
```

When asking for merge and again immediately before merging, the orchestrator
greps the current title, body and all commit messages with that pattern and
checks `gh pr view <PR> --json closingIssuesReferences`: neither a closing
reference nor a closing link to the measured issue may remain. This API check
is mandatory even if the grep finds nothing. A Development-sidebar link can
close it too; `gh` has no unlink command, so remove it in the PR's Development
sidebar. If a link cannot be removed, or a keyword in a commit message needs
a history rewrite and a new head, the merge waits and the orchestrator tells
the maintainer. A rewritten head follows the normal
[push and review rules](README.md#lifecycle).

The orchestrator checks that the measured issue is open immediately after
merge, when posting the pending entry, and when recording the judgement.
If it was auto-closed, reopen it at once and record the repair on the issue;
recovery never replaces the closing-reference and closing-link prevention.

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
  error, stop, compare no tree, request no measurement approval and report to
  the maintainer.
- On exit 0, the actual tree from `git rev-parse <squash>^{tree}`, both tree
  values and the comparison result. Compare the expected tree with the actual
  tree, not the reviewed head's tree directly with the squash tree: unrelated
  changes on `main` may have arrived since review. If equal, no extra review
  of the squash integration is needed; the existing PR reviews and the squash
  commit's own CI remain required. If different, the actual difference
  `git diff <expected-tree> <actual-tree>` needs an independent post-merge
  check by an AI that wrote none of the PR or the commits added to `main`
  since review ([independence](README.md#principles)). Establish authorship
  from the orchestration record as in [reviewing](reviewing.md#review-independence-and-rounds);
  if independence cannot be established, stop and report to the maintainer.
  The checker reports in an issue comment naming the full expected and actual
  tree SHAs, the diff and its verdict. The open-PR
  [follow-up procedure](review-followups.md#copilot-follow-up-check) does not
  govern this post-merge check. All existing review requirements remain;
  anything substantive or not judgeable stops the procedure and is reported
  to the maintainer under the
  [substantive-delta rule](review-operations.md#third-ai-review).
- CI on the squash commit itself and its run URL. The required check is the
  `ci` job of the `ci.yml` push workflow run on that exact full SHA; the
  ruleset requires only `ci`. Locate it with
  `gh api --method GET repos/tkakkie/ribbitto/actions/runs -f head_sha=<full-squash-SHA>`;
  verify workflow, event and head SHA, then inspect the job with
  `gh api repos/tkakkie/ribbitto/actions/runs/<id>/jobs?filter=latest`
  (quote the endpoint in the shell). Record the run URL, full head SHA,
  `run_attempt`, and the run and `ci` job statuses and conclusions. Judge the
  latest attempt; after a rerun, fetch the run metadata and jobs again and
  confirm they cover that attempt and SHA. A run listing or runs on `main`
  alone do not prove the job passed at this commit.
  - `failure`: inspect the failed jobs and logs and identify the cause.
  - `cancelled`: identify the cancellation's cause, including whether a newer
    `main` push cancelled it (`ci.yml` cancels in-progress runs on `main`).
    Cancellation alone does not justify a rerun.
  - `missing`: confirm no matching push run or `ci` job can be found; stop
    and report to the maintainer.

  Only one rerun of the failed jobs (`gh run rerun <run-id> --failed`) is
  allowed for a known timing flake, and only when no newer run on `main` is
  in progress. Record the cause and evidence for applying that rule; no other
  automatic rerun is allowed. If success cannot be confirmed, stop and report
  to the maintainer; do not request measurement approval until the latest
  attempt of that exact SHA's push run and its `ci` job are completed with
  `success`. A later push cannot produce a run at this SHA, and a successful
  run at another SHA never substitutes.

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

Update only the status column, dating each change; never rewrite an existing
entry's SHA columns to match a result. A changed base/head SHA pair keeps the
old entry as history and creates a new entry requiring new approval before
running. Issue comments are the authoritative state
and result record: the pending entry, approval, explicit start instruction and
result. Record chat approvals and start instructions there with their exact
scope, quoting both full target SHAs in the approval record. A result whose
SHAs differ from the pending entry is invalid.

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
