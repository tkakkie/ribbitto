# Running Argon

Argon is Gemini 4 Argon (High) in Antigravity's `agy` CLI. The Go launcher
`cmd/argonreview` runs its adversarial reviews (#1065 series). Which pull
requests get one is [adversarial-review.md](adversarial-review.md)'s
business; this file covers the setup, the invocation and the boundary.

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
  git -C "$src" fetch --quiet origin &&
  git -C "$src" checkout --quiet --detach origin/main &&
  test -z "$(git -C "$src" status --porcelain --ignored)" &&
  (cd "$src" && go build -o "$RIBBITTO_ARGON_HOME/bin/argonreview" ./cmd/argonreview) &&
  "$RIBBITTO_ARGON_HOME/bin/argonreview" review <pr-number>
```

The `status` check stops on any local change, untracked or ignored file,
which the build would otherwise compile. That clean checkout is the
boundary: never build or run a pull request's copy of the launcher. Every
subcommand but `guard` also runs `verify`, which refuses a binary not built
from that commit with a clean tree (`go run` included); it catches a stale
or accidental build, not a malicious one, whose own code could skip it.

| Subcommand | Does |
|---|---|
| `verify` | prints the trusted revision, or refuses |
| `prepare` | writes the launcher's three files in the dedicated `HOME` and refuses customisation |
| `check` | `prepare`, then `agy models` under the supervisor: Argon must be listed |
| `prompt <pr>` | prints the prompt a review would send, and leaves nothing |
| `review <pr>` | the review; prints the report only if the run counts |
| `probe <repo> <prompt-file>` | the same pipeline on a scratch repository's `HEAD` with a trusted prompt; prints `agy`'s output and the verdict |
| `guard` | the `PreToolUse` hook; Antigravity calls it, not you |

`RIBBITTO_ARGON_TIMEOUT` (a Go duration, default `25m`) limits `agy`. A
review or a probe exits 0 for a run that counts, 124 for the time limit,
130 or 143 for INT or TERM, a failing `agy`'s own status, and 1 for
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
   --print-timeout … --output-format stream-json` runs under the supervisor,
   in the checkout, with the dedicated `HOME` and a `TMPDIR` inside the
   workspace.
6. The run does not count on any of: a non-zero exit or the time limit;
   `AGY_ERROR`; a malformed, incomplete or duplicate-keyed transcript; a
   result that is not a `SUCCESS`, has no response or an empty one, or lists
   `denied_actions`; a changed tree; changed shared settings; a process
   cleanup the supervisor cannot confirm; a workspace it cannot remove.
7. The workspace is removed, then the report is written, headed with the
   target and the launcher revision.

## The boundary

Deny rules refuse file writes, commands, URL reads and MCP. The guard, the
only hook, allows `view_file`, `list_dir`, `grep_search` and `find_by_name`
and denies everything else, including `search_web`, subagents,
`generate_image` and `schedule`, which no deny rule can name. Reads outside
the checkout are refused because nothing grants `read_file`.

Remaining limits:

- `AGENTS.md` and `GEMINI.md` in the checkout are loaded as instructions: a
  pull request can change what the reviewer is told, not what it can do.
- Antigravity treats a hook that prints nothing and exits 0 as an allow.
  The guard exits 0 only after writing an allow, so a lost answer is a
  refusal (#1130).
- A guard refusal ends the tool call, not the run; such a run can still
  count. The refusal stays in the transcript.
- A process running as you can change the dedicated `HOME` between
  `prepare` and the launch; it could change your own configuration anyway.
- The supervisor stops every process in `agy`'s session; a descendant that
  calls `setsid` leaves it and is not stopped.

The workspace is always removed. The dedicated `HOME` keeps the sign-in,
the launcher's three files and Antigravity's own state (logs, conversation
store, caches); `prepare` refuses customisation there but does not police
the rest.

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
