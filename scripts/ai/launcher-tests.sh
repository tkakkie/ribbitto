#!/usr/bin/env bash
# Runs the AI launchers' self-tests (grok-review_test.sh, then
# muse-review_test.sh), which take most of make check's time.
#
#   launcher-tests.sh               always runs them (make check-ai)
#   launcher-tests.sh --base REV    runs them only when the change since REV
#                                   can affect them (make check)
#   launcher-tests.sh --base ''     always runs them (CI on main and nightly)
#
# With a base, the change is what differs from the merge base of REV and
# HEAD: commits, staged, unstaged and untracked files.
# It can affect the self-tests when any path, old or new, is under
# scripts/ai/ (this script included), .github/prompts/ or .github/workflows/,
# or is the Makefile. The selection fails safe: when the base cannot be
# resolved or listing the changes fails, the self-tests run. It prints what it chose and why,
# and a self-test failure is its exit status.
set -euo pipefail
dir=$(cd "$(dirname "$0")" && pwd)
cd "$dir/../.."

run() {
  echo "launcher self-tests: run ($*)"
  bash "$dir/grok-review_test.sh"
  bash "$dir/muse-review_test.sh"
}

usage() {
  echo 'usage: launcher-tests.sh [--base REV]' >&2
  exit 2
}
if (($# == 0)); then
  run always
  exit 0
fi
[[ $# == 2 && $1 == --base ]] || usage
base=$2
if [[ -z $base ]]; then
  run 'always: no base given'
  exit 0
fi

if ! base_commit=$(git rev-parse --verify -q --end-of-options "$base^{commit}"); then
  run "fail safe: cannot resolve the base '$base'"
  exit 0
fi
if ! merge_base=$(git merge-base "$base_commit" HEAD); then
  run "fail safe: no merge base of '$base' and HEAD"
  exit 0
fi
# NUL-separated, so no path is quoted; --no-renames lists both the old and
# the new path of a rename.
changes() {
  git diff -z --name-only --no-renames "$merge_base" &&
    git diff -z --name-only --no-renames --cached "$merge_base" &&
    git ls-files -z --others --exclude-standard
}
if ! changed=$(changes | tr '\0' '\n'); then
  run "fail safe: could not list the changes since '$base'"
  exit 0
fi

# Matched in bash, with no grep, sort or here-string: a failure of any of
# them (#560) could otherwise pass for "nothing matched" and skip the tests.
matched=
IFS=$'\n'
set -f
for path in $changed; do
  case $path in
    scripts/ai/* | .github/prompts/* | .github/workflows/* | Makefile)
      case $'\n'$matched in
        *$'\n'"$path"$'\n'*) ;;
        *) matched+=$path$'\n' ;;
      esac
      ;;
  esac
done
set +f
IFS=$' \t\n'
if [[ -n $matched ]]; then
  matched=${matched%$'\n'}
  run "changed since '$base': ${matched//$'\n'/ }"
else
  echo "launcher self-tests: skipped (nothing changed since '$base' under scripts/ai/, .github/prompts/ or .github/workflows/, nor the Makefile; make check-ai runs them)"
fi
