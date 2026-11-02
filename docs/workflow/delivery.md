Read when: implementers need the standard brief or scan a PR body before publishing; orchestrators start or wake an implementer.

# Delivering a pull request

## Standard implementer brief

Paste into every implementer run (once `ready`), with the issue number, branch,
risk and issue text:

> You are `<Codex|Argon>`, implementing issue `#<N>` in `tkakkie/ribbitto` on
> branch `<ai>/<topic>` from `origin/main` (`ready`, risk: `<normal|high>`).
>
> Issue `#<N>` — `<title>`:
> `<issue body, including Done when>`
>
> 1. Read `AGENTS.md` and only the touched area's `docs/` (`AGENTS.md` "Where
>    things are explained"). Use only your own token; never put a credential in
>    a commit, pull request, comment or log; use only a disposable local test
>    database (`RIBBITTO_TEST_DATABASE_URL`), never a shared one. Skip
>    `docs/workflow/` unless an action triggers a section:
>    own token, credentials, `ready`, merge (docs/workflow/README.md#what-no-ai-does),
>    tool failures, disposable test DBs (docs/workflow/ai-credentials.md#tool-failures-and-disposable-databases),
>    PR body credential scan (docs/workflow/delivery.md#pr-body-credential-scan), or
>    measurement caps (docs/workflow/investigations.md#investigations-and-the-performance-target).
> 2. Implement the issue in this worktree (diff under ~400 lines excluding
>    generated files). Stop and report if a maintainer decision or a new
>    language/tool is needed. For a behaviour-preserving conversion of a script to a module, before opening the PR run a test or script that exercises a copy of the old script (taken from the baseline SHA, since the conversion deletes it) and the new module on the same inputs and compares outputs — clock, storage failures, malformed values included — (`node --test` in `web/test` for DOM-free logic, the existing `TestBrowser` suites (or a new case in them) as the comparison when real browser behaviour is needed); the PR body names the baseline SHA and records the commands and the result (kept as evidence there, not as a permanent file); a restructuring with no old/new pair records what was compared instead ([#2](https://github.com/tkakkie/ribbitto/issues/2#issuecomment-6559340753)).
> 3. Run `make check` and `make prepush` (plus `make deps` if imports change);
>    fix failures without deleting, skipping or weakening a test.
> 4. Commit (Conventional Commits, `Co-Authored-By:` trailer), push only your
>    branch, run the credential scan (docs/workflow/delivery.md#pr-body-credential-scan)
>    on the PR body file, and open a DRAFT PR (`.github/pull_request_template.md`,
>    `Closes #<N>`).
> 5. Wait for CI (`gh pr checks`), fix failures (`make prepush` before any
>    follow-up push), and stop once CI is green. Do not set labels, request or
>    run reviews, mark ready, merge, or touch `main`, other branches, settings
>    or tokens.

## Waking the orchestrator

Start every implementer run as one blocking background command (no `nohup` or
trailing `&`), so its end or time limit wakes the orchestrator (#1457).
On wakeup, read the result: with a PR, continue through the parallel reviews,
fixes and follow-up checks to `ai-reviewed` until a rule in this file,
[`reviewing.md`](reviewing.md), [review operations](review-operations.md),
[review follow-ups](review-followups.md) or the [README](README.md) says to ask the
maintainer. If the implementer stopped for a maintainer decision, report at
once; if the run failed or timed out, inspect the worktree and branch and
resume or restart once, else report. CI and Copilot wait loops cap at 60
minutes; at the cap, report to the maintainer and do not proceed. The
maintainer decides `ready`, the merge, measurement approval and the cases where
the rules say to ask.

## PR body credential scan

- Before publishing a PR body (`gh pr create --body-file body.md`), run this
  scan; it prints nothing, or each match is reviewed (redact real credentials;
  harmless placeholders or prose may stay). It joins `\`-continued lines and
  flags URLs with user info (#358):

  ```sh
  sed -e :a -e '/\\$/{' -e '$!N' -e '}' -e 's/\\\n//' -e ta body.md |
    grep -niE '://[^/@[:space:]]*@|password|passwd|secret|token|(api|access|private)[-_[:space:]]?key'
  ```

