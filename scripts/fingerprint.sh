#!/usr/bin/env bash
# Prints a fingerprint of the worktree's uncommitted state: the tracked diff,
# then hashes of the untracked file list and of each untracked file. make
# prepush compares it before and after generation (#848). Paths travel
# NUL-separated, through a file rather than a pipe, and after --, so a quoted
# name, a file named "-" or a failed hash makes this script fail instead of
# dropping a file out of the comparison (Grok on #855).
set -euo pipefail
list=$(mktemp)
trap 'rm -f "$list"' EXIT
git diff --binary --no-ext-diff
git ls-files -z -o --exclude-standard >"$list"
git hash-object -- "$list"
if [[ -s $list ]]; then
  xargs -0 git hash-object -- <"$list"
fi
