#!/usr/bin/env bash
# Adversarial review of a pull request by Muse Code, read-only.
#
# Usage (from the maintainer's checkout; runs the launcher as it is on main,
# never a copy a pull request could have changed):
#   git fetch origin main &&
#     launcher=$(git show origin/main:scripts/ai/muse-review.sh) &&
#     bash -c "$launcher" muse-review <pr-number>
# (not `bash <(git show …)`: if git show failed, bash would run an empty
# script and exit 0, so a failed fetch would look like a clean review)
#
# Checks out the PR head in a temporary git worktree, builds a prompt from the
# trusted .github/prompts/adversarial.md on origin/main plus the PR title,
# description and diff, runs Muse Code with read-only tools, prints the report and
# removes everything it created. See docs/workflow/running-other-ai.md.
#
# Environment:
#   RIBBITTO_MUSE_TIMEOUT     seconds before Muse Code is stopped (1–86400, default 1200)
#   RIBBITTO_MUSE_TRUSTED_REF git ref the launcher and prompt must come from (default
#                             origin/main); change it only to test a PR that edits them
#   RIBBITTO_MUSE_TEST_SETUP_DELAY  seconds (0–5, default 0) the supervisor's child
#                             waits after creating Muse Code's process group; tests
#                             only, to deliver signals during that setup
#
# Exit status: 0 on success; 124 on timeout; 130/143 when interrupted; Muse Code's
# own status when Muse Code fails; 126 if Muse Code's process group could not be
# created; 1 for any other error.
set -euo pipefail

die() { echo "muse-review: $*" >&2; exit 1; }

[[ $# -eq 1 && $1 =~ ^[1-9][0-9]*$ ]] || die "usage: $0 <pr-number>"
pr=$1
for cmd in muse gh git jq perl; do
  command -v "$cmd" >/dev/null || die "$cmd is not installed or not on PATH"
done
timeout=${RIBBITTO_MUSE_TIMEOUT:-1200}
# Bounded so Perl's alarm() can represent it (a huge value would wrap to 0,
# which disables the deadline).
if ! [[ $timeout =~ ^[1-9][0-9]{0,4}$ ]] || ((timeout > 86400)); then
  die "RIBBITTO_MUSE_TIMEOUT must be an integer from 1 to 86400 (seconds), got '$timeout'"
fi
trusted_ref=${RIBBITTO_MUSE_TRUSTED_REF:-origin/main}
setup_delay=${RIBBITTO_MUSE_TEST_SETUP_DELAY:-0}
[[ $setup_delay =~ ^[0-5]$ ]] \
  || die "RIBBITTO_MUSE_TEST_SETUP_DELAY must be an integer from 0 to 5 (seconds), got '$setup_delay'"

repo=$(git rev-parse --show-toplevel) || die "could not locate the repository"
git -C "$repo" fetch --quiet origin main || die "could not fetch main from origin"

# Fail closed if this launcher was run from a file that differs from the
# trusted one. This only catches an accidentally edited copy: a malicious
# copy runs its own code before reaching this check, so the security
# boundary is the documented invocation (bash -c with the blob from
# origin/main), which never executes a file a pull request can change.
self=${BASH_SOURCE[0]:-}
if [[ -f $self ]]; then
  trusted=$(git -C "$repo" rev-parse --verify --quiet "$trusted_ref:scripts/ai/muse-review.sh") \
    || die "$trusted_ref has no scripts/ai/muse-review.sh"
  [[ $(git hash-object "$self") == "$trusted" ]] \
    || die "refusing to run: $self differs from $trusted_ref:scripts/ai/muse-review.sh. Run: launcher=\$(git show $trusted_ref:scripts/ai/muse-review.sh) && bash -c \"\$launcher\" muse-review $pr"
fi
tmp=$(mktemp -d "${TMPDIR:-/tmp}/muse-review.XXXXXX")
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
    echo "muse-review: could not remove worktree $worktree; run 'git worktree prune'" >&2
    [[ $status -eq 0 ]] && status=1
  fi
  if ! git -C "$repo" worktree prune; then
    echo "muse-review: 'git worktree prune' failed; stale worktree metadata may remain" >&2
    [[ $status -eq 0 ]] && status=1
  fi
  if ! rm -rf "$tmp"; then
    echo "muse-review: could not remove $tmp" >&2
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
# the same head even if the PR is pushed in the meantime.
gh pr view "$pr" --json title,body,baseRefName,baseRefOid,headRefOid >"$tmp/pr.json" \
  || die "could not read PR #$pr with gh (does it exist?)"
head_sha=$(jq -er .headRefOid "$tmp/pr.json") || die "PR #$pr has no head commit"
base_sha=$(jq -er .baseRefOid "$tmp/pr.json") || die "PR #$pr has no base commit"
base_ref=$(jq -er .baseRefName "$tmp/pr.json") || die "PR #$pr has no base branch"

git -C "$repo" fetch --quiet origin "refs/heads/$base_ref" "refs/pull/$pr/head" \
  || die "could not fetch $base_ref and PR #$pr from origin"
for sha in "$base_sha" "$head_sha"; do
  git -C "$repo" cat-file -e "$sha^{commit}" 2>/dev/null \
    || die "commit $sha of PR #$pr is not available (the PR changed while starting); run again"
done

# Refuse symlinks anywhere in the PR tree before building the prompt or
# checking anything out: a link could expose files outside the workspace
# (keys, tokens), even with writing and the shell disabled.
symlinks=$(git -C "$repo" ls-tree -r --full-tree "$head_sha" | awk '$1 == "120000" { sub(/^[^\t]*\t/, ""); print }') \
  || die "could not list the files of $head_sha"
if [[ -n $symlinks ]]; then
  printf 'muse-review: refusing to review PR #%s: it contains symlinks, which could expose files outside the worktree:\n%s\n' \
    "$pr" "$symlinks" >&2
  exit 1
fi
# The prompt comes from main, never from the PR under review, so a PR cannot
# rewrite its own review instructions.
git -C "$repo" show "$trusted_ref:.github/prompts/adversarial.md" >"$tmp/prompt.md" \
  || die "$trusted_ref has no .github/prompts/adversarial.md"
git -C "$repo" diff "$base_sha...$head_sha" >"$tmp/pr.diff" \
  || die "could not compute the diff of PR #$pr"
git -C "$repo" worktree add --quiet --detach "$worktree" "$head_sha" \
  || die "could not check out $head_sha"

title=$(jq -er .title "$tmp/pr.json") \
  || die "could not read PR title"
body=$(jq -r '.body // ""' "$tmp/pr.json") \
  || die "could not read PR description"
# Everything the PR controls goes into one JSON object on the last line of
# the prompt. JSON escaping means no title, description or diff can end the
# payload early or add text after it, unlike fixed delimiters.
payload=$(jq -cn --argjson number "$pr" --arg base "$base_sha" --arg head "$head_sha" \
  --arg title "$title" --arg description "$body" --rawfile diff "$tmp/pr.diff" \
  '{pull_request: $number, base_commit: $base, head_commit: $head,
    title: $title, description: $description, diff: $diff}') \
  || die "could not encode PR #$pr for the prompt"
printf '\nUNTRUSTED_PAYLOAD_JSON: %s\n' "$payload" >>"$tmp/prompt.md"

# Keep the preflight's Standard model and read-only sandbox flags explicit.
muse_args=(
  exec --model muse-spark-1.3 --workspace "$worktree" --json
  --disable-write --disable-shell --disable-web-tools
  --sandbox-network restricted --no-session-log --no-foreign-personal-context
  --max-model-steps 30 --prompt-file "$tmp/prompt.md"
)

# macOS has no `timeout`, and `perl -e 'alarm…; exec…'` would only signal
# Muse Code itself. This supervisor runs Muse Code in its own process group and signals
# the whole group (TERM, then KILL after a grace period) on expiry, on INT or
# TERM, and also after Muse Code exits, so no descendant outlives the run.
# Signals are blocked until the group exists and the handlers are installed.
# It exits with Muse Code's status, 124 on timeout, 130/143 when interrupted, and
# 126 if the process group could not be created.
# Stdin is closed: headless CLIs can otherwise wait for input forever (#2).
perl -e '
  use strict; use warnings;
  use POSIX qw(:signal_h :sys_wait_h setpgid _exit);
  my ($limit, $setup_delay, @cmd) = @ARGV;
  my $block = POSIX::SigSet->new(SIGINT, SIGTERM, SIGALRM);
  my $old = POSIX::SigSet->new;
  sigprocmask(SIG_BLOCK, $block, $old) or die "muse-review: sigprocmask: $!\n";
  # Only the child calls setpgid, then confirms through the pipe before it
  # execs. The parent setting the group as well raced with the child on
  # macOS (EPERM, or ESRCH once the child had exited), so it never does (#82).
  # Perl marks the pipe close-on-exec, so Muse Code does not inherit it.
  pipe(my $group_ready, my $confirm) or die "muse-review: pipe: $!\n";
  my $pid = fork() // die "muse-review: fork failed: $!\n";
  if ($pid == 0) {
    close $group_ready;
    setpgid(0, 0) or _exit(126);
    sleep $setup_delay if $setup_delay;
    syswrite($confirm, "1") == 1 or _exit(126);
    close $confirm;
    sigprocmask(SIG_SETMASK, $old);
    { no warnings qw(exec); exec { $cmd[0] } @cmd; }
    print STDERR "muse-review: cannot run $cmd[0]: $!\n";
    _exit(127);
  }
  close $confirm;
  my $n;
  do { $n = sysread($group_ready, my $byte, 1) } while (!defined $n && $!{EINTR});
  close $group_ready;
  # EOF without the confirmation byte: the child failed, or was killed, before
  # it confirmed. It had not exec-ed, so it started nothing, and reaping it
  # leaves nothing behind.
  if (!$n) {
    waitpid($pid, 0);
    print STDERR "muse-review: could not create a process group for Muse Code\n";
    exit 126;
  }
  my $deadline = time + $limit;
  my $reap_group = sub {
    return unless kill 0, -$pid;
    kill "TERM", -$pid;
    for (1 .. 50) { last unless kill 0, -$pid; select(undef, undef, undef, 0.1) }
    kill "KILL", -$pid if kill 0, -$pid;
  };
  my $stop = sub {
    my ($code, $why) = @_;
    print STDERR "muse-review: $why\n";
    $reap_group->();
    waitpid($pid, 0);
    exit $code;
  };
  # Reap before acting on a signal: Muse Code may have exited during the last poll
  # interval. Signals during group cleanup must not replace its status either.
  my $stop_request;
  $SIG{ALRM} = sub { $stop_request //= [124, "timed out after $limit s; stopped Muse Code"] };
  $SIG{INT}  = sub { $stop_request //= [130, "interrupted; stopped Muse Code"] };
  $SIG{TERM} = sub { $stop_request //= [143, "terminated; stopped Muse Code"] };
  alarm $limit;
  sigprocmask(SIG_SETMASK, $old);
  while (waitpid($pid, WNOHANG) != $pid) {
    $stop->(@$stop_request) if $stop_request;
    # alarm is still a backstop; wall time also catches a deadline that passed
    # during machine sleep, even if the alarm did not advance with it.
    $stop->(124, "timed out after $limit s; stopped Muse Code") if time >= $deadline;
    select undef, undef, undef, 2;
  }
  my $status = $? & 127 ? 128 + ($? & 127) : $? >> 8;
  alarm 0;
  $reap_group->();
  exit $status;
' "$timeout" "$setup_delay" muse "${muse_args[@]}" </dev/null >"$tmp/muse.jsonl" &
supervisor=$!
status=0
wait "$supervisor" || status=$?
supervisor=""
case $status in
  0) ;;
  124)
    next=$(( timeout * 2 > 86400 ? 86400 : timeout * 2 ))
    if [[ -t 2 ]]; then printf '\033[0m' >&2; fi
    echo "muse-review: Muse Code did not finish within ${timeout}s" >&2
    if (( next > timeout )); then
      printf 'muse-review: rerun with RIBBITTO_MUSE_TIMEOUT=%s using the invocation in docs/workflow/running-other-ai.md\n' "$next" >&2
    else
      echo 'muse-review: the 86400-second maximum is already in effect' >&2
    fi
    exit 124 ;;
  *) echo "muse-review: Muse Code failed (exit $status)" >&2; exit "$status" ;;
esac

# Validate the entire stream before emitting any report: even a later model
# change or malformed event must prevent a successful review.
jq -R -s -j '
  split("\n") | if last == "" then .[:-1] else . end
  | map(fromjson)
  | if any(.[]; type != "object") then error("JSONL events must be objects") else . end
  | [.[] | select(.payload_type == "run.model.configured")] as $models
  | if ($models | length) == 0 then error("missing run.model.configured events")
    elif any($models[]; .payload.model_id != "muse-spark-1.3") then
      error("every configured model must be muse-spark-1.3")
    else . end
  | [.[] | select(.payload_type == "run.output.delta")]
  | if any(.[]; (.payload.text | type) != "string" or (.sequence | type) != "number") then
      error("invalid run.output.delta text or sequence")
    else . end
  | sort_by(.sequence) | map(.payload.text) | join("")
  | if length == 0 then error("no run.output.delta text") else . end
' "$tmp/muse.jsonl" || die "could not validate Muse Code JSONL report"
