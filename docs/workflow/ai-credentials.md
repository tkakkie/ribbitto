[ -s "$tok" ] || { echo "run-as: no token, or an empty token file, for $name" >&2; exit 2; }# AI credentials and launchers

How each AI gets its own GitHub token and how Codex and Argon are started
(rules: [README](README.md#tokens)). Tested on 2026-10-31; the redacted
evidence is on #2002.

## Layout

`~/.ribbitto-ai/` (mode 700), outside every repository: `claude.token`,
`codex.token`, `antigravity.token` (mode 600) and two scripts (mode 700).
The maintainer creates the tokens and saves each without showing it to an AI
(zsh: `read -rs "t?token: "; (umask 077; printf '%s' "$t" > <name>.token); unset t`).

`credential-helper.sh`: answers only for `https://github.com`, and finds the token
directory from its own path, so it works under any `HOME`.

```sh
#!/bin/sh
[ "$2" = get ] || exit 0
input=$(cat)
protocol=$(printf '%s\n' "$input" | sed -n 's/^protocol=//p')
host=$(printf '%s\n' "$input" | sed -n 's/^host=//p')
[ "$protocol" = https ] && [ "$host" = github.com ] || exit 0
dir=$(cd "$(dirname "$0")" && pwd)
printf 'username=x-access-token\npassword=%s\n' "$(cat "$dir/$1.token")"
```

`run-as.sh <name> <command…>`: the empty `credential.helper` clears the
inherited `osxkeychain` before the helper is added; no global configuration
changes.

```sh
#!/bin/sh
name=$1; shift
tok="$HOME/.ribbitto-ai/$name.token"
[ -r "$tok" ] || { echo "run-as: no token file for $name" >&2; exit 2; }
GH_TOKEN=$(cat "$tok"); export GH_TOKEN
unset GITHUB_TOKEN GH_ENTERPRISE_TOKEN GITHUB_ENTERPRISE_TOKEN
export GIT_TERMINAL_PROMPT=0 GIT_CONFIG_COUNT=2
export GIT_CONFIG_KEY_0=credential.helper GIT_CONFIG_VALUE_0=
export RIBBITTO_AI_HELPER="$HOME/.ribbitto-ai/credential-helper.sh" RIBBITTO_AI_NAME="$name"
export GIT_CONFIG_KEY_1=credential.helper GIT_CONFIG_VALUE_1='!"$RIBBITTO_AI_HELPER" "$RIBBITTO_AI_NAME"'
exec "$@"
```

Claude uses the same launcher: `run-as.sh claude gh …` and `run-as.sh claude git push …`.
Git runs a helper string through a shell, so the helper path and the AI name
are exported once and the single-quoted string only names the variables: the
path stays data, whatever characters `HOME` holds, and it survives a later
change of `HOME` (the test used a path with `$`, `"` and spaces).

## Argon (Antigravity)

- Use the root-owned pin `/usr/local/argon-agy/agy` by absolute path with its
  directory first in `PATH`: `agy` replaces the file it was started from when
  it updates itself; the tested pin sits in a root-owned directory the AI user
  cannot write to. Check its hash before and after a run.
- A dedicated home, `~/dev/ribbitto/agy-home`, signed in by the maintainer
  (`HOME="$HOME/dev/ribbitto/agy-home" /usr/local/argon-agy/agy`, interactive).
  Never use the maintainer's own `~/.gemini`.
- `settings.json` of that home:

  ```json
  {"permissions": {"allow": ["command(*)", "write_file(*)"]},
   "trustedWorkspaces": ["<HOME>/dev/ribbitto/wt"]}
  ```

  Replace `<HOME>` with the absolute path of the home directory: `agy` does
  not expand it, and an unmatched path leaves the worktree untrusted.
  Without the allow rules a command is auto-denied in headless mode.
  `--mode auto` is not available for the account. `--dangerously-skip-permissions`
  was not used: Claude Code's automatic safety check refuses to run it.
  `agy` runs the hook commands of a trusted workspace's `.agents/hooks.json`,
  so the trusted path is the worktree folder, not the whole home.
- Start `run-as.sh` before `HOME` is replaced, and pass `GIT_AUTHOR_NAME`,
  `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_NAME` and `GIT_COMMITTER_EMAIL` (the
  README command does) because that home has no `.gitconfig`.

## Checks that show no fallback

- `gh auth status` says not logged in; the keychain holds no `github.com` or
  `gh:github.com` item; `ssh -T git@github.com` is refused.
- A plain `git push` fails with prompts disabled; the same push through
  `run-as.sh` succeeds. A helper that returns a wrong password fails with
  `Authentication failed`, so the keychain is not consulted.
- Never print a token, a header, `env` output or a `GIT_TRACE` log.
