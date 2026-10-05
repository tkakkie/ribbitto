#!/usr/bin/env bash
# Tests scripts/ai/launcher-tests.sh in a disposable repository whose two
# self-tests are fakes that record that they ran: a change under each listed
# path (committed, staged, unstaged or untracked; a deletion; a rename away)
# runs them, an unrelated change skips them and says so, an unresolvable base
# or a failing git runs them, and a failing self-test fails the script.
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fail() {
  echo "FAIL $*" >&2
  exit 1
}

repo=$work/repo
mkdir -p "$repo/scripts/ai" "$repo/.github/prompts" "$repo/.github/workflows" "$repo/docs"
cp "$root/scripts/ai/launcher-tests.sh" "$repo/scripts/ai/"
for suite in grok muse; do
  printf '#!/usr/bin/env bash\necho %s >> "$RAN"\n[[ ${FAIL_SUITE-} != %s ]]\n' "$suite" "$suite" \
    > "$repo/scripts/ai/$suite-review_test.sh"
done
for file in Makefile scripts/ai/grok-review.sh .github/prompts/adversarial.txt \
  .github/workflows/ci.yml docs/a.txt; do
  echo base > "$repo/$file"
done
git -C "$repo" init -q
git -C "$repo" config user.name 'Launcher test'
git -C "$repo" config user.email 'launcher-test@example.invalid'
git -C "$repo" add -A
git -C "$repo" -c commit.gpgsign=false commit -q -m base
git -C "$repo" branch -q base

export RAN=$work/ran
out=$work/out
# Each case starts from the base commit with a clean tree.
reset() {
  git -C "$repo" checkout -q -f -B case base
  git -C "$repo" clean -q -fdx
  rm -f "$RAN"
}
commit() { git -C "$repo" add -A && git -C "$repo" -c commit.gpgsign=false commit -q -m "$1"; }
check() { (cd "$repo" && bash scripts/ai/launcher-tests.sh "$@") > "$out" 2>&1; }
# expect_run <case> <text the output must contain> [args...]
expect_run() {
  local name=$1 text=$2
  shift 2
  check "$@" || { cat "$out" >&2; fail "$name: exited non-zero"; }
  grep -qF -- 'launcher self-tests: run' "$out" || fail "$name: did not choose to run: $(cat "$out")"
  grep -qF -- "$text" "$out" || fail "$name: output lacks '$text': $(cat "$out")"
  [[ $(cat "$RAN" 2>/dev/null) == $'grok\nmuse' ]] || fail "$name: the self-tests did not both run"
}

reset
echo '# change' >> "$repo/docs/a.txt"
echo new > "$repo/docs/b.txt"
echo near > "$repo/Makefile.local"
mkdir -p "$repo/sub" "$repo/scripts/aid"
echo near > "$repo/sub/Makefile"
echo near > "$repo/scripts/aid/x.sh"
commit 'unrelated'
echo staged >> "$repo/docs/b.txt"
git -C "$repo" add docs/b.txt
echo untracked > "$repo/docs/c.txt"
check --base base || { cat "$out" >&2; fail "unrelated change: exited non-zero"; }
grep -qF "launcher self-tests: skipped (nothing changed since 'base'" "$out" || fail "unrelated change: not skipped: $(cat "$out")"
[[ ! -e $RAN ]] || fail "unrelated change: a self-test ran"
echo "PASS an unrelated change, including near-miss paths, skips them and says so"

for file in scripts/ai/grok-review.sh scripts/ai/launcher-tests.sh .github/prompts/adversarial.txt \
  .github/workflows/ci.yml Makefile; do
  reset
  echo '# change' >> "$repo/$file"
  commit "change $file"
  expect_run "committed $file" "changed since 'base': $file" --base base
done
reset
echo new > "$repo/.github/prompts/new.txt"
commit 'add a prompt'
expect_run 'added file' '.github/prompts/new.txt' --base base
echo "PASS a commit under each listed path runs them and names it"

reset
git -C "$repo" rm -q .github/prompts/adversarial.txt
commit 'delete a prompt'
expect_run 'deletion' '.github/prompts/adversarial.txt' --base base
reset
git -C "$repo" mv .github/workflows/ci.yml docs/ci.yml
commit 'rename a workflow away'
expect_run 'rename' '.github/workflows/ci.yml' --base base
echo "PASS a deleted or renamed-away listed file runs them"

reset
echo staged >> "$repo/Makefile"
git -C "$repo" add Makefile
expect_run 'staged' "changed since 'base': Makefile" --base base
reset
echo staged >> "$repo/Makefile"
git -C "$repo" add Makefile
echo base > "$repo/Makefile"
expect_run 'staged, then undone in the tree' "changed since 'base': Makefile" --base base
reset
echo unstaged >> "$repo/scripts/ai/grok-review.sh"
expect_run 'unstaged' 'scripts/ai/grok-review.sh' --base base
reset
echo untracked > "$repo/.github/workflows/new.yml"
expect_run 'untracked' '.github/workflows/new.yml' --base base
echo "PASS staged, unstaged and untracked changes count"

reset
echo '# change' >> "$repo/Makefile"
commit 'base moved'
git -C "$repo" branch -q -f moved
git -C "$repo" checkout -q -B case base
echo '# change' >> "$repo/docs/a.txt"
commit 'unrelated'
check --base moved || { cat "$out" >&2; fail "a base ahead of the branch: exited non-zero"; }
grep -qF 'skipped' "$out" || fail "a base ahead of the branch: $(cat "$out")"
[[ ! -e $RAN ]] || fail "a base ahead of the branch: a self-test ran"
echo "PASS changes on the base after the merge base do not count"

reset
expect_run 'unknown base' "fail safe: cannot resolve the base 'no-such-ref'" --base no-such-ref
reset
git -C "$repo" checkout -q --orphan unrelated
commit 'unrelated history'
expect_run 'no merge base' "fail safe: no merge base of 'base' and HEAD" --base base
reset
mkdir -p "$work/bin"
real_git=$(command -v git)
printf '#!/usr/bin/env bash\n[[ $1 != diff ]] || exit 128\nexec %q "$@"\n' "$real_git" > "$work/bin/git"
chmod +x "$work/bin/git"
PATH=$work/bin:$PATH expect_run 'failing diff' "fail safe: git could not list the changes since 'base'" --base base
echo "PASS an unresolvable base, unrelated history and a failing git diff run them"

reset
expect_run 'no base' 'always: no base given' --base ''
reset
expect_run 'no arguments' 'run (always)'
if check --base; then fail "a missing base was accepted"; fi
echo "PASS without a base they always run"

for suite in grok muse; do
  reset
  if FAIL_SUITE=$suite check; then fail "a failing $suite self-test passed (always)"; fi
  reset
  echo '# change' >> "$repo/Makefile"
  if FAIL_SUITE=$suite check --base base; then fail "a failing $suite self-test passed (selected)"; fi
  reset
  if FAIL_SUITE=$suite check --base no-such-ref; then fail "a failing $suite self-test passed (fail safe)"; fi
done
echo "PASS a failing self-test fails the script"
