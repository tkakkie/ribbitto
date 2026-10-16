# Argon's pull requests

How a pull request [Argon implemented](argon-implementer.md) is reviewed
and verified where it differs from the usual rules
([reviewing](reviewing.md), [Copilot](copilot.md),
[adversarial review](adversarial-review.md)), which otherwise all apply.
Approved on [#1247](https://github.com/tkakkie/ribbitto/issues/1247).

## Who reviews

- **Cross-review:** Claude; Claude also does the Copilot follow-up check of
  Argon's fixes, and Codex checks a change Claude writes
  ([who does what](argon-implementer.md#who-does-what)).
- **Adversarial review:** tier B, Codex, with the fixed invocation below;
  never Argon, which never reviews its own work. Tier C, none. A diff that
  reaches tier A leaves Argon, and the maintainer decides.
- **After the adversarial review:** the adversarial follow-up checks
  ([reviewing](reviewing.md)) and every re-review the
  [completion rule](adversarial-review.md#which-review-runs) requires are
  Codex's: a follow-up check as in
  [running the other AI](running-other-ai.md), with `$base` the reviewed
  SHA; a re-review with the fixed invocation on the new head. The
  sequencing, head and CI checks, dispositions, and the failure and retry
  rules are unchanged; only the reviewer is Codex instead of Argon.

The template's *Adversarial* field says `Codex (B) at <SHA>`. Claude posts
the report as `**Adversarial review — Codex (tier B)** (at <SHA>)`, each
finding with a disposition.

## The fixed invocation

Codex runs read-only, under a time limit, in a clean worktree of the
current `origin/main`, never in Argon's tree. Its instructions are the
trusted `.github/prompts/adversarial.md` from `origin/main` with three
adaptations and nothing else: the working directory is the base, the
head's changes come from the supplied diff, and nothing is executed. The
`UNTRUSTED_PAYLOAD_JSON:` line follows, as in the
[Argon launcher](running-argon.md#what-a-review-does), with the head's CI
results added. Copy this block from `origin/main`, never from a pull
request; `limit` is in [running the other AI](running-other-ai.md).

```sh
# $W: Argon's worktree; $clean: a clean worktree, detached at origin/main
# just fetched; P: the pull request. gh pr checks fails unless CI is
# green, which the review needs first.
g() { env -i PATH=/usr/bin:/bin HOME=/nonexistent GIT_CONFIG_GLOBAL=/dev/null \
  GIT_CONFIG_NOSYSTEM=1 git -C "$W" -c core.hooksPath=/dev/null \
  -c core.fsmonitor=false "$@"; }
d=$(mktemp -d) &&
  gh pr view P --json number,title,body,baseRefOid,headRefOid > "$d/pr" &&
  base=$(jq -r .baseRefOid "$d/pr") && head=$(jq -r .headRefOid "$d/pr") &&
  test "$head" = "$(g rev-parse HEAD)" &&
  test "$(git -C "$clean" rev-parse HEAD)" = "$(git -C "$clean" rev-parse origin/main)" &&
  test -z "$(git -C "$clean" status --porcelain --untracked-files=all)" &&
  gh pr checks P > /dev/null &&
  gh api "repos/tkakkie/ribbitto/commits/$head/check-runs?per_page=100" \
    --jq '[.check_runs[] | {name, status, conclusion}]' > "$d/ci" &&
  g diff --no-ext-diff --no-textconv "$base...$head" > "$d/diff" &&
  git -C "$clean" cat-file blob origin/main:.github/prompts/adversarial.md > "$d/in" &&
  cat >> "$d/in" <<'EOF' &&

## Adaptations for this review

These three replace what the text above says about the working directory;
everything else above stands.

- The working directory is a clean checkout of the base (origin/main), not
  of the pull request's head.
- Read the head's changes from the payload's `diff` (base_commit...head_commit),
  and its CI results from `ci_checks`.
- Execute nothing: no build, test, script, `go` or `git` command. Read and
  search files only.
EOF
  printf 'UNTRUSTED_PAYLOAD_JSON: ' >> "$d/in" &&
  jq -c --rawfile diff "$d/diff" --slurpfile ci "$d/ci" '{pull_request: .number,
    base_commit: .baseRefOid, head_commit: .headRefOid, title,
    description: .body, diff: $diff, ci_checks: $ci[0]}' "$d/pr" >> "$d/in" &&
  test "$head" = "$(gh pr view P --json headRefOid -q .headRefOid)" &&
  (cd "$clean" && limit 1800 codex exec -m gpt-6-astra -s read-only \
    -o "$d/report.md" - < "$d/in")
```

The head must equal the pull request's `headRefOid` at the start and
just before Codex runs, and the CI results are those of that head. A
failed, timed-out or empty run does not count
([completion](adversarial-review.md#which-review-runs)).

## Mutation evidence

Argon runs nothing, and no AI runs its code on the host, so where its pull
request needs deliberate-defect evidence (a test-only pull request or a
regression test for an adversarial finding, [reviewing](reviewing.md), or
its issue's *Done when*), Claude prepares it on CI with as few runs as
possible:

1. **One scratch draft pull request,** never merged, reviewed or labelled,
   branched from the Argon pull request's head in a worktree of Claude's
   own, never Argon's, with the hardened git settings
   ([boundary](argon-implementer.md#the-boundary)). Its one commit holds
   every deliberate defect, in the code under test. Each defect is
   independent, breaks one behaviour and is checked by its own named
   subtest, and is meant to fail an assertion, not the build.
2. **One CI run** must show each expected subtest failing. Claude records,
   in the real pull request's *Verified by*, each injected defect (file,
   line and what it breaks) and the subtest and assertion it made fail,
   with a link to the run.
3. **Extra runs only where needed:** a defect whose failure did not show,
   or was masked (by a panic, a build failure or another defect), gets its
   own commit and run, recorded the same way.
4. **Reverted:** the real pull request's green CI on its head is the
   evidence that the tests pass without the defects. Claude closes the
   scratch pull request and links it from the real one.

If a later commit changes the tests or the code under test, Claude repeats
this for the behaviours it touches. Code Claude or Codex wrote keeps local
per-defect checks.
