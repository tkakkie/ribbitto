#!/usr/bin/env bash
# Tests scripts/fingerprint.sh (or the script given as $1) in a disposable
# repository: every change to an untracked or tracked file changes the
# fingerprint, including names git quotes, a file named "-" and files after
# either, and an unreadable file fails it rather than dropping out (#855).
# Ignored files and an unchanged tree leave it unchanged.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
script=${1:-$root/scripts/fingerprint.sh}
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
