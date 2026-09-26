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
#   RIBBITTO_GROK_TIMEOUT     seconds before Grok is stopped (1–86400, default 1200)
#   RIBBITTO_GROK_MODEL       model id passed to `grok -m` (default: the CLI default; see `grok models`)
#   RIBBITTO_GROK_PROMPT_REF  git ref to read the prompt from (default origin/main); change it
#                             only to test a PR that edits the prompt itself
#
# Exit status: 0 on success; 124 on timeout; 130/143 when interrupted; Grok's
# own status when Grok fails; 1 for any other error.
set -euo pipefail

die() { echo "grok-review: $*" >&2; exit 1; }

[[ $# -eq 1 && $1 =~ ^[1-9][0-9]*$ ]] || die "usage: $0 <pr-number>"
pr=$1
for cmd in grok gh git jq perl; do
  command -v "$cmd" >/dev/null || die "$cmd is not installed or not on PATH"
done
timeout=${RIBBITTO_GROK_TIMEOUT:-1200}
# Bounded so Perl's alarm() can represent it (a huge value would wrap to 0,
# which disables the deadline).
if ! [[ $timeout =~ ^[1-9][0-9]{0,4}$ ]] || ((timeout > 86400)); then
  die "RIBBITTO_GROK_TIMEOUT must be an integer from 1 to 86400 (seconds), got '$timeout'"
fi
prompt_ref=${RIBBITTO_GROK_PROMPT_REF:-origin/main}

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

# One API call gives a consistent snapshot of the PR: title, description and
# the exact base and head commits. The diff is then computed locally from
# those two commits, so the prompt, the worktree and the diff always describe
# the same head even if someone pushes meanwhile.
gh pr view "$pr" --json title,body,baseRefName,baseRefOid,headRefOid >"$tmp/pr.json" \
  || die "could not read PR #$pr with gh (does it exist?)"
head_sha=$(jq -er .headRefOid "$tmp/pr.json") || die "PR #$pr has no head commit"
base_sha=$(jq -er .baseRefOid "$tmp/pr.json") || die "PR #$pr has no base commit"
base_ref=$(jq -er .baseRefName "$tmp/pr.json") || die "PR #$pr has no base branch"

git -C "$repo" fetch --quiet origin main "refs/heads/$base_ref" "refs/pull/$pr/head" \
  || die "could not fetch main, $base_ref and PR #$pr from origin"
for sha in "$base_sha" "$head_sha"; do
  git -C "$repo" cat-file -e "$sha^{commit}" 2>/dev/null \
    || die "commit $sha of PR #$pr is not available (the PR changed while starting); run again"
done

# The prompt comes from main, never from the PR under review, so a PR cannot
# rewrite its own review instructions.
git -C "$repo" show "$prompt_ref:.github/prompts/adversarial.md" >"$tmp/prompt.md" \
  || die "$prompt_ref has no .github/prompts/adversarial.md"
git -C "$repo" diff "$base_sha...$head_sha" >"$tmp/pr.diff" \
  || die "could not compute the diff of PR #$pr"
git -C "$repo" worktree add --quiet --detach "$worktree" "$head_sha" \
  || die "could not check out $head_sha"

{
  printf '\n---\n\nPull request #%s: base %s, head %s.\n\n' "$pr" "$base_sha" "$head_sha"
  printf '<<<PR_TITLE (untrusted)\n%s\nPR_TITLE>>>\n\n' "$(jq -r .title "$tmp/pr.json")"
  printf '<<<PR_DESCRIPTION (untrusted)\n%s\nPR_DESCRIPTION>>>\n\n' "$(jq -r '.body // ""' "$tmp/pr.json")"
  printf '<<<PR_DIFF (untrusted)\n'
  cat "$tmp/pr.diff"
  printf 'PR_DIFF>>>\n'
} >>"$tmp/prompt.md"

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
# Grok itself. This supervisor runs Grok in its own process group and signals
# the whole group (TERM, then KILL after a grace period) on expiry, on INT or
# TERM, and also after Grok exits, so no descendant outlives the run.
# Signals are blocked until the group exists and the handlers are installed.
# It exits with Grok's status, 124 on timeout, 130/143 when interrupted.
# Stdin is closed: headless CLIs can otherwise wait for input forever (#2).
perl -e '
  use strict; use warnings;
  use POSIX qw(:signal_h setpgid _exit);
  my ($limit, @cmd) = @ARGV;
  my $block = POSIX::SigSet->new(SIGINT, SIGTERM, SIGALRM);
  my $old = POSIX::SigSet->new;
  sigprocmask(SIG_BLOCK, $block, $old) or die "grok-review: sigprocmask: $!\n";
  my $pid = fork() // die "grok-review: fork failed: $!\n";
  if ($pid == 0) {
    setpgid(0, 0) or _exit(126);
    sigprocmask(SIG_SETMASK, $old);
    { no warnings qw(exec); exec { $cmd[0] } @cmd; }
    print STDERR "grok-review: cannot run $cmd[0]: $!\n";
    _exit(127);
  }
  # Set the group from the parent as well, so it exists before any signal is
  # sent; EACCES only means the child has already exec-ed with its own group.
  if (!setpgid($pid, $pid) && !$!{EACCES}) {
    kill "KILL", $pid; waitpid($pid, 0);
    die "grok-review: could not create a process group for Grok: $!\n";
  }
  my $reap_group = sub {
    return unless kill 0, -$pid;
    kill "TERM", -$pid;
    for (1 .. 50) { last unless kill 0, -$pid; select(undef, undef, undef, 0.1) }
    kill "KILL", -$pid if kill 0, -$pid;
  };
  my $stop = sub {
    my ($code, $why) = @_;
    print STDERR "grok-review: $why\n";
    $reap_group->();
    waitpid($pid, 0);
    exit $code;
  };
  $SIG{ALRM} = sub { $stop->(124, "timed out after $limit s; stopped Grok") };
  $SIG{INT}  = sub { $stop->(130, "interrupted; stopped Grok") };
  $SIG{TERM} = sub { $stop->(143, "terminated; stopped Grok") };
  alarm $limit;
  sigprocmask(SIG_SETMASK, $old);
  waitpid($pid, 0);
  my $status = $? & 127 ? 128 + ($? & 127) : $? >> 8;
  alarm 0;
  $reap_group->();
  exit $status;
' "$timeout" grok "${grok_args[@]}" </dev/null &
supervisor=$!
status=0
wait "$supervisor" || status=$?
supervisor=""
case $status in
  0) ;;
  124) echo "grok-review: Grok did not finish within ${timeout}s" >&2; exit 124 ;;
  *) echo "grok-review: Grok failed (exit $status)" >&2; exit "$status" ;;
esac
