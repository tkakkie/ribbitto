# Running Argon

Argon is Gemini 4 Argon (High) in Antigravity's `agy` CLI. The Go launcher
`cmd/argonreview` runs issue reviews, adversarial reviews and Copilot
follow-up checks (#1065 series, #1270). [Adversarial selection](adversarial-review.md)
and [Copilot assignments](copilot.md) define when to use them; this file
covers setup, invocation and the boundary.

## One-time setup

- **A dedicated `HOME`.** Pick a directory for Antigravity's state, outside
  your own `~/.gemini`, and export its absolute path as
  `RIBBITTO_ARGON_HOME`. `prepare` refuses your home, an ancestor of it, and
  anything overlapping its `.gemini`.
- **Sign in once** there, interactively: `HOME="$RIBBITTO_ARGON_HOME" agy`.
  The token stays in that `HOME`; `prepare` keeps it.
- **Telemetry and AI credits are off by default.** The launcher's
  `settings.json` sets neither, and the maintainer confirmed both off in that
  `HOME`'s `/config` (#1070).
- **Keychain prompt.** After a token refresh, macOS may say the keychain for
  `antigravity` was not found. Answer **Cancel**: `agy` falls back to file
  storage in that `HOME`. Never answer *Reset to Defaults*, which resets
  your login keychain.
- **A source checkout.** A clone of `tkakkie/ribbitto` whose `origin` is
  GitHub, used for nothing else, exported as `RIBBITTO_ARGON_SOURCE`. The
  launcher compares its own build with that checkout's `origin/main`, never
  with the current directory or the pull request.

Your own `~/.gemini/antigravity-cli/settings.json` is never loaded or
changed; a review reads it only to hash it, and fails if the hash changes.

## The trusted invocation

Build from a clean `origin/main` of the source checkout, outside its tree,
then run:

```sh
src=$RIBBITTO_ARGON_SOURCE &&
  pinned() { env -i PATH="$PATH" GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_NO_REPLACE_OBJECTS=1 git -C "$src" "$@"; } &&
  pinned fetch --quiet https://github.com/tkakkie/ribbitto.git +refs/heads/main:refs/remotes/origin/main &&
  replaced=$(pinned for-each-ref refs/replace) && test -z "$replaced" &&
  pinned checkout --quiet --detach origin/main &&
  tree_status=$(pinned status --porcelain --ignored --untracked-files=all) &&
    test -z "$tree_status" &&
  (cd "$src" && env -i PATH="$PATH" HOME="$HOME" GOENV=off GIT_CONFIG_GLOBAL=/dev/null \
    GIT_CONFIG_NOSYSTEM=1 GIT_NO_REPLACE_OBJECTS=1 \
    go build -o "$RIBBITTO_ARGON_HOME/bin/argonreview" ./cmd/argonreview) &&
  "$RIBBITTO_ARGON_HOME/bin/argonreview" review <pr-number>
```

For a Claude-written issue, use the same trusted build and invocation,
replacing the final command with:

```sh
"$RIBBITTO_ARGON_HOME/bin/argonreview" issue <number> [round]
```

The round defaults to 1; only 1 and 2 are accepted. If the run does not
count, fall back to Codex; the two-round limit spans both reviewers
([reviewing](reviewing.md)). Record whether Argon counted or fell back in
the issue review comment.

`pinned` runs git with an empty environment but `PATH`, so no inherited
`GIT_*` variable or `GIT_CONFIG_*` setting applies, and with global and
system configuration and replace refs off, as the launcher's own git calls
do. A replace ref in the clone stops the run before the checkout. It fetches
from the pinned URL, so no `insteadOf` rule outside the clone can
substitute another repository (Copilot and Codex on #1136). Keep the
clone's own configuration, and any file it includes, free of `url.*`
rules. The `status` check stops on any local change, untracked or ignored
file, which could otherwise affect the build. `go build` runs with the same
git settings and an empty environment but `PATH` and `HOME`, with `GOENV`
off, so no `GOFLAGS` or Go environment file of yours applies (Argon on
#1136); its module downloads are checked against `go.sum`. That clean checkout is the
boundary: never build or run a pull request's copy of the launcher. Every
subcommand but `guard` also runs `verify`, which refuses a binary not built
from that commit with a clean tree (`go run` included); it catches a stale
or accidental build, not a malicious one, whose own code could skip it.

| Subcommand | Does |
|---|---|
| `verify` | prints the trusted revision, or refuses |
| `prepare` | writes the launcher's four files in the dedicated `HOME` and refuses customisation |
| `check` | `prepare`, then `agy models` under the supervisor: Argon must be listed |
| `prompt <pr>` | prints the prompt a review would send, and leaves nothing |
| `review <pr>` | the review; prints the report only if the run counts |
| `issue <number> [round]` | reviews an issue against the trusted revision; prints the report only if the run counts |
| `copilot-check <pr> <approved-sha>` | checks Copilot follow-up changes at the current PR head; prints only a counted report |
| `probe <repo> <prompt-file>` | the same pipeline on a scratch repository's `HEAD` with a trusted prompt; prints `agy`'s output and the verdict |
| `guard` | the `PreToolUse` hook; Antigravity calls it, not you |

`RIBBITTO_ARGON_TIMEOUT` (a Go duration, default `25m`) limits `agy`. A
review, Copilot check or probe exits 0 for a run that counts,
124 for the time limit, 130 or 143 for INT or TERM, a failing `agy`'s own status, and 1 for
anything else, with the reason on stderr. Bad arguments exit 2. `guard`
exits 0 only for an allow, and non-zero for a deny or a failed write.

## What a review does

1. `prepare`, then a hash of the shared settings.
2. One `gh pr view` snapshot; the base and head are fetched from the pinned
   GitHub URL into a temporary repository of its own, never a worktree of
   the source, so the source's hooks, `insteadOf`, filters and replace refs
   cannot touch the pull request's tree.
3. A head not on the pull request, or a tree with a symlink, is refused
   before checkout. Every `.agents` entry is removed: a pull request's
   `.agents/hooks.json` would run shell commands outside the sandbox.
4. The prompt is `.github/prompts/adversarial.md` from the trusted revision
   plus one `UNTRUSTED_PAYLOAD_JSON` line with the original diff.
5. The tree is recorded; `agy -p … --model gemini-4-argon-high --sandbox
   --print-timeout … --output-format stream-json --agent argon-reviewer` runs under the supervisor,
   in the checkout, with the dedicated `HOME` and a `TMPDIR` inside the
   workspace.
6. The run does not count on any of: a non-zero exit or the time limit;
   `AGY_ERROR`; a malformed, incomplete or duplicate-keyed transcript; a
   result that is not a `SUCCESS`, has no response or an empty one, or lists
   `denied_actions`; a changed tree; changed shared settings; a process
   cleanup the supervisor cannot confirm; a workspace it cannot remove.
7. The workspace is removed, then the report is written, headed with the
   target and the launcher revision.

For `issue`, steps 2–4 instead take one `gh issue view` snapshot of the
title, body and comments, check out the trusted revision in a fresh
repository with the same symlink refusal and `.agents` removal, and read
`.github/prompts/issue-review.md` from that revision. The issue snapshot is
one `UNTRUSTED_PAYLOAD_JSON` line, never instructions. Steps 1 and 5–7,
including every run-counting rule, are identical.

For `copilot-check <pr> <approved-sha>`, use the same trusted invocation.
One GraphQL snapshot holds the PR text, head and Copilot review bodies and
inline comments; truncated connections are refused. The workspace uses
`newWorkspace` as `review` does, then refuses a fetched PR head different
from the snapshot or a full approved SHA that is not its ancestor. The
trusted `.github/prompts/copilot-check.md` and one `UNTRUSTED_PAYLOAD_JSON`
line with the snapshot and `<approved-sha>..<head>` diff replace the prompt.
All run-counting rules stay. On Claude's and Codex's pull requests, a failed
or non-counting run falls back to the AI that did not write the follow-up
change: Codex for Claude's, Claude for Codex's
([Copilot](copilot.md#copilot-follow-up-check)). Argon's own pull requests,
adversarial follow-up checks and closure verification keep their reviewers.

## The boundary

`agy` receives only the dedicated `HOME`, workspace `TMPDIR` (none for
`check`, which writes nothing, uses shared `/tmp`), `PATH`,
`LANG`, the POSIX `LC_` names, `USER` and `LOGNAME`. Proxy and certificate
variables are not passed: behind a proxy, `agy` may not connect until a pull
request adds them to the list. Record a live `check` and
`issue`/`review` run from the trusted build.

The [review agent](argon-review-agent.md) lists exactly `view_file`,
`list_dir`, `grep_search` and `find_by_name`. Its tools list does not
establish what else `agy` offers; the linked page records MCP tools that
escape that list, the deny rules, the guard (the only hook), outside-read
refusals and the remaining limits.

Reports appear only after workspace removal. The dedicated `HOME` keeps
the sign-in, the launcher's four files and Antigravity's state (logs,
conversation store, caches); `prepare` refuses customisation there but
does not police the rest.

## Evidence

- #1041: the preflights and four trial reviews with the trial runner.
- #1071: probes with the launcher from `origin/main` at 8e59f5e. Reads
  outside the checkout (absolute and `..`) were refused by the deny rule;
  writes, shell, URL fetch, `search_web`, subagents, `generate_image` and
  `schedule` by the guard; a symlinked tree before launch. A sentinel never
  appeared in any output, and the tree and the shared settings were
  unchanged.
- #1071 and #1130: a broken hook that printed nothing and exited 0 let
  `search_web` run; the guard now denies by its exit status, rechecked from
  `origin/main` at 42ff887.

For #1263, pre-merge fixture tests cover the trusted issue checkout, absence
of pull request content and `.agents` removal; the guard and deny rules
are unchanged, so #1071's probes still cover tool denial. After merge,
record one live `issue` run from the trusted build on #1263, including
whether it counted.

For #1270, fixture tests cover the current PR head, approved SHA ancestry,
isolated checkout with `.agents` removed, and one JSON payload line. Guard
and deny rules are unchanged. After merge, record the first live
`copilot-check` run on #1270 with its duration and whether it counted or
fell back.

For #1501, pre-merge tests cover agent bytes and refusals. The recorded
`argonreview probe` on a scratch repository (checkout reads through
`view_file`, `list_dir`, `grep_search`; `read_file`, a write, a command and
an outside read refused) is host work after the PR is reviewed. Record which
tools the model is actually told it has, and list `config/agents` after the run.
