#!/usr/bin/env bash
# Tests scripts/fingerprint.sh and scripts/check-unchanged.sh (or the scripts
# given as $1 and $2) in a disposable repository. Every change to an untracked
# or tracked file changes the fingerprint, including names git quotes, a file
# named "-" and files after either, and an unreadable file fails it rather
# than dropping out (#855); ignored files and an unchanged tree leave it
# unchanged. check-unchanged.sh fails when the command changes a file, when
# the command fails, and when either fingerprint fails even though its output
# is unchanged.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
script=${1:-$root/scripts/fingerprint.sh}
unchanged=${2:-$root/scripts/check-unchanged.sh}
work=$(mktemp -d)
trap 'chmod -R u+rw "$work" 2>/dev/null; rm -rf "$work"' EXIT
fail() {
  echo "FAIL $*" >&2
  exit 1
}

repo=$work/repo
mkdir -p "$repo"
git -C "$repo" init -q
git -C "$repo" config user.name 'Fingerprint test'
git -C "$repo" config user.email 'fingerprint-test@example.invalid'
printf 'ignored.txt\n' >"$repo/.gitignore"
printf 'tracked\n' >"$repo/tracked.txt"
git -C "$repo" add .gitignore tracked.txt
git -C "$repo" -c commit.gpgsign=false commit -q -m 'Baseline'
fingerprint() { (cd "$repo" && bash "$script"); }
# changes <description> <command>: the command must change the fingerprint,
# and both fingerprints must succeed.
changes() {
  local before after
  before=$(fingerprint) || fail "$1: fingerprint failed before the change"
  (cd "$repo" && eval "$2")
  after=$(fingerprint) || fail "$1: fingerprint failed after the change"
  [[ $after != "$before" ]] || fail "$1: fingerprint unchanged"
}
same() {
  local before after
  before=$(fingerprint) || fail "$1: fingerprint failed"
  (cd "$repo" && eval "$2")
  after=$(fingerprint) || fail "$1: fingerprint failed"
  [[ $after == "$before" ]] || fail "$1: fingerprint changed"
}

same 'unchanged tree' ':'
changes 'tracked edit' 'printf more >>tracked.txt'
changes 'new untracked file' 'printf a >plain.txt'
changes 'rewritten untracked file' 'printf b >plain.txt'
changes 'new file in an untracked directory' 'mkdir -p gen && printf a >gen/one.go'
changes 'second file in that directory' 'printf a >gen/two.go'
changes 'renamed untracked file' 'mv gen/two.go gen/three.go'
same 'ignored file' 'printf a >ignored.txt'
echo "PASS tracked, untracked, directory, rename and ignored changes"

changes 'new quoted name' 'printf a >café.txt'
changes 'rewritten quoted name' 'printf b >café.txt'
changes 'name with a quote, backslash and tab' "printf a >'q\"b\\\\c'\$'\\t''d.txt'"
changes 'file named -' 'printf a >./-'
changes 'file after -' 'printf c >plain.txt'
changes 'file after a quoted name' 'printf d >zz_after.txt'
changes 'rewritten file after a quoted name' 'printf e >zz_after.txt'
echo "PASS quoted names, a file named - and files after them"

printf secret >"$repo/unreadable.txt"
chmod 000 "$repo/unreadable.txt"
if [[ -r $repo/unreadable.txt ]]; then
  echo "SKIP unreadable file: running with permission to read it"
else
  if fingerprint >/dev/null 2>&1; then
    fail "an unreadable untracked file did not fail the fingerprint"
  fi
  echo "PASS an unreadable untracked file fails"
fi

# check-unchanged.sh with the real fingerprint, beside it as in scripts/.
real=$work/real
mkdir -p "$real"
cp "$unchanged" "$real/check-unchanged.sh"
cp "$script" "$real/fingerprint.sh"
chmod u+r "$repo/unreadable.txt" 2>/dev/null || true
rm -f "$repo/unreadable.txt"
check() { (cd "$repo" && bash "$1/check-unchanged.sh" "${@:2}") >/dev/null 2>&1; }
check "$real" true || fail "check-unchanged failed on a command that changes nothing"
if check "$real" sh -c 'printf a >generated.go'; then
  fail "check-unchanged passed a command that created a file"
fi
if check "$real" sh -c 'printf b >generated.go'; then
  fail "check-unchanged passed a command that rewrote an untracked file"
fi
if check "$real" false; then
  fail "check-unchanged passed a failing command"
fi
echo "PASS check-unchanged: unchanged, created, rewritten and failing commands"

# A fingerprint that fails on one call but prints the same output each time.
stub=$work/stub
mkdir -p "$stub"
cp "$unchanged" "$stub/check-unchanged.sh"
cat >"$stub/fingerprint.sh" <<'STUB'
calls=$(($(cat "$STUB_CALLS") + 1))
echo "$calls" >"$STUB_CALLS"
echo same
[[ $calls != "$STUB_FAIL" ]] || exit 42
STUB
export STUB_CALLS=$work/calls
for failing in 1 2; do
  echo 0 >"$STUB_CALLS"
  if STUB_FAIL=$failing check "$stub" true; then
    fail "check-unchanged passed although fingerprint call $failing failed"
  fi
done
echo 0 >"$STUB_CALLS"
STUB_FAIL=0 check "$stub" true || fail "check-unchanged failed with both fingerprints succeeding"
echo "PASS check-unchanged fails when either fingerprint fails"
