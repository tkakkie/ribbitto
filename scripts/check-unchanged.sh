#!/usr/bin/env bash
# Runs the given command and fails if it changed the worktree's uncommitted
# state as scripts/fingerprint.sh sees it, so changes already uncommitted do
# not fail it. make prepush runs make generate css through it (#848). Each
# fingerprint's own status is checked: a failed one must not pass as an
# unchanged tree (Grok on #855).
set -euo pipefail
fingerprint=$(dirname "$0")/fingerprint.sh
before=$(bash "$fingerprint") || exit 1
"$@"
after=$(bash "$fingerprint") || exit 1
if [[ $after != "$before" ]]; then
  echo "$* changed files; commit them:"
  git status --short
  exit 1
fi
