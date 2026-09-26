#!/usr/bin/env bash
# Adversarial review of a pull request by Grok, read-only.
#
# Usage: scripts/ai/grok-review.sh <pr-number>
#
# Checks out the PR head in a temporary git worktree, builds a prompt from the
# trusted .github/prompts/adversarial.md on origin/main plus the PR title,
# description and diff, runs Grok with read-only tools, prints the report and
# removes everything it created. See docs/workflow.md#adversarial-review.
#
# Environment:
#   RIBBITTO_GROK_TIMEOUT  seconds before Grok is stopped (positive integer, default 1200)
#   RIBBITTO_GROK_MODEL    model id passed to `grok -m` (default: the CLI default; see `grok models`)
#   RIBBITTO_GROK_PROMPT_REF  git ref to read the prompt from (default origin/main); change it
#                          only to test a PR that edits the prompt itself
set -euo pipefail

die() { echo "grok-review: $*" >&2; exit 1; }

[[ $# -eq 1 && $1 =~ ^[1-9][0-9]*$ ]] || die "usage: $0 <pr-number>"
pr=$1
for cmd in grok gh git perl; do
  command -v "$cmd" >/dev/null || die "$cmd is not installed or not on PATH"
done
timeout=${RIBBITTO_GROK_TIMEOUT:-1200}
prompt_ref=${RIBBITTO_GROK_PROMPT_REF:-origin/main}
[[ $timeout =~ ^[1-9][0-9]*$ ]] || die "RIBBITTO_GROK_TIMEOUT must be a positive integer (seconds), got '$timeout'"

repo=$(git rev-parse --show-toplevel)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/grok-review.XXXXXX")
worktree="$tmp/worktree"
supervisor=""

# Runs on every exit path we can catch (success, failure, timeout, INT, TERM;
# SIGKILL cannot be caught). Keeps the original exit status and reports, but
# does not hide, cleanup failures.
cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [[ -n $supervisor ]] && kill -0 "$supervisor" 2>/dev/null; then
    kill -TERM "$supervisor" 2>/dev/null || true
    wait "$supervisor" 2>/dev/null || true
  fi
  if [[ -d $worktree ]] && ! git -C "$repo" worktree remove --force "$worktree" 2>/dev/null; then
    echo "grok-review: could not remove worktree $worktree; run 'git worktree prune'" >&2
    [[ $status -eq 0 ]] && status=1
  fi
  git -C "$repo" worktree prune 2>/dev/null || true
  if ! rm -rf "$tmp"; then
    echo "grok-review: could not remove $tmp" >&2
    [[ $status -eq 0 ]] && status=1
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# The prompt comes from main, never from the PR under review, so a PR cannot
# rewrite its own review instructions.
git -C "$repo" fetch --quiet origin main "pull/$pr/head" \
  || die "could not fetch PR #$pr from origin (does it exist?)"
git -C "$repo" show "$prompt_ref:.github/prompts/adversarial.md" >"$tmp/prompt.md" \
  || die "$prompt_ref has no .github/prompts/adversarial.md"

meta=$(gh pr view "$pr" --json title,body,headRefOid --jq '[.headRefOid, .title] | @tsv') \
  || die "could not read PR #$pr with gh"
head_sha=${meta%%$'\t'*}
git -C "$repo" worktree add --quiet --detach "$worktree" "$head_sha" \
  || die "could not check out $head_sha"

{
  printf '\n---\n\nPull request #%s at %s.\n\n' "$pr" "$head_sha"
  printf '<<<PR_TITLE (untrusted)\n%s\nPR_TITLE>>>\n\n' "${meta#*$'\t'}"
  printf '<<<PR_DESCRIPTION (untrusted)\n'
  gh pr view "$pr" --json body --jq .body
  printf 'PR_DESCRIPTION>>>\n\n<<<PR_DIFF (untrusted)\n'
  gh pr diff "$pr"
  printf 'PR_DIFF>>>\n'
} >>"$tmp/prompt.md" || die "could not read the description or diff of PR #$pr"

grok_args=(
  --prompt-file "$tmp/prompt.md"
  --output-format plain
  # Read-only: plan mode and an allow-list of read-only built-in tools.
  --permission-mode plan
  --tools 'read_file,list_dir,grep'
  --disable-web-search
  --max-turns 40
  --cwd "$worktree"
)
if [[ -n ${RIBBITTO_GROK_MODEL:-} ]]; then
  grok_args+=(-m "$RIBBITTO_GROK_MODEL")
fi

# macOS has no `timeout`, and `perl -e 'alarm…; exec…'` would only signal
# Grok itself. This supervisor runs Grok in its own process group and, on
# expiry or when asked to stop, signals the whole group (TERM, then KILL), so
# no Grok descendant survives. Exit 124 means the time limit was hit.
# Stdin is closed: headless CLIs can otherwise wait for input forever (#2).
perl -e '
  use POSIX qw(setpgid);
  my ($limit, @cmd) = @ARGV;
  my $pid = fork() // die "grok-review: fork failed: $!\n";
  if ($pid == 0) {
    setpgid(0, 0);
    exec(@cmd) or do { print STDERR "grok-review: cannot run $cmd[0]: $!\n"; POSIX::_exit(127) };
  }
  my $stop = sub {
    my ($code, $why) = @_;
    print STDERR "grok-review: $why\n" if $why;
    kill "TERM", -$pid; sleep 5; kill "KILL", -$pid;
    waitpid($pid, 0); exit $code;
  };
  $SIG{ALRM} = sub { $stop->(124, "timed out after $limit s; stopped Grok") };
  $SIG{INT}  = sub { $stop->(130, "interrupted") };
  $SIG{TERM} = sub { $stop->(143, "terminated") };
  alarm $limit;
  waitpid($pid, 0);
  exit($? & 127 ? 128 + ($? & 127) : $? >> 8);
' "$timeout" grok "${grok_args[@]}" </dev/null &
supervisor=$!
status=0
wait "$supervisor" || status=$?
supervisor=""
case $status in
  0) ;;
  124) echo "grok-review: Grok did not finish within ${timeout}s" >&2; exit 124 ;;
  *) die "Grok failed (exit $status)" ;;
esac
