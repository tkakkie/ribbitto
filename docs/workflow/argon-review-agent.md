# Argon review agent

Reviews, issue reviews, Copilot checks and probes launch with
`--agent argon-reviewer`; `--model` still selects `gemini-4-argon-high`.
The custom agent's `tools` list names exactly `view_file`, `list_dir`,
`grep_search` and `find_by_name`. This does not establish that `agy` offers
nothing else. With MCP configured, `manage_task`, `call_mcp_tool`,
`list_resources` and `read_resource` escape a custom agent's `tools` list
([#1427 spike part 2](argon-verify.md#tool-permissions), also recorded for the
[implementer](argon-implementer.md#the-boundary)). Reviews keep
`config/mcp_config.json` empty, retain the `mcp(*)` deny rule and use the
guard to refuse all four escape tools.

Deny rules refuse writes, commands, URL reads and MCP. The guard, the
only hook, allows `view_file`, `list_dir`, `grep_search` and `find_by_name`
and denies everything else, including `read_file`. It is the only layer
that can refuse `search_web`, subagents, `generate_image` and `schedule`,
which no deny rule can name. The agent lists no `read_file` and nothing
grants it; deny rules and headless mode refuse reads outside the checkout.
The host probe records those refusals and which tools the model is actually
told it has.

## Preparation

`prepare` owns four files beneath the dedicated HOME's `.gemini`:
`antigravity-cli/settings.json`, `config/hooks.json`,
`config/mcp_config.json` and `config/agents/argon-reviewer.md`.
The agent's static bytes come from trusted code, never PR or issue input.
Every review, issue review, Copilot check, probe and `check` starts with
`prepare`, which rewrites and byte-compares all four files on every run.
Reviews may run in parallel: `prepare` serialises every configuration scan
and write on an exclusive lock at the dedicated HOME's `.argonreview.lock`.
After about two minutes of waiting, `prepare` refuses and names the lock file.
The lock file stays at rest, created empty by `prepare` on first use and left
even when `prepare` refuses; the kernel releases the lock when the process exits.
Each review also verifies read-only under the same lock that the dedicated
HOME still holds exactly the four byte-checked files just before `agy` launches
and after it exits; failure before launch starts no `agy`, and failure afterwards
means the run does not count.
Verification never creates the lock file and refuses when it is missing.
These pre-launch and post-run checks are point-in-time checks under the lock.
The lock is not held across the run, which would serialise all reviews, so a
concurrent `prepare` from a trusted launcher may write identical agent bytes
through its temporary file while `agy` runs. The post-run check still refuses
any state other than exactly the four byte-checked files.
Reviews sharing a HOME must use the same launcher build: `hooks.json` contains
the guard path, so different builds write different bytes and refuse each other.
A leftover `.argonreview-<pid>` refusal means a killed run's file only when
every writer takes the lock: a launcher built before the lock was added,
or another tool writing there, does not wait and may still be writing.
Wait and retry before removing it, and check no argonreview is running first.

The agent stays at rest. This is simpler and stricter: one mechanism,
nothing to remove, and no per-run placement window. Every other entry in
`.gemini/config/agents` is refused as customisation, including hidden files,
subdirectories, other agents and case aliases. The refusal names the entry:
customisation could add tools, hooks or instructions; remove it by hand if
it is a leftover (a killed run's `.argonreview-<pid>` temporary file or a
`.DS_Store`). Owned directories and files
refuse symlinks and non-directory/non-regular types respectively. Nested
`agents` is opened through its parent's descriptor; the account's shared
agents directory and agent file have the same overlap and alias protections
as the other owned configuration.

## Remaining limits

- `AGENTS.md` and `GEMINI.md` in the checkout are loaded as instructions: a
  pull request can change what the reviewer is told, not what it can do.
- Antigravity treats a hook that prints nothing and exits 0 as an allow.
  The guard exits 0 only after writing an allow, so a lost answer is a
  refusal (#1130).
- A guard refusal ends the tool call, not the run, which can still count;
  the refusal stays in the transcript.
- A process running as you can still change the dedicated `HOME` during
  the run. The post-run check detects changes left there but cannot prevent
  them or detect changes restored before the check.
- The supervisor stops every process in `agy`'s session; a descendant that
  calls `setsid` leaves it and is not stopped.

See [running Argon](running-argon.md#evidence) for recorded probes and the
pending #1501 host probe. Pre-merge tests do not establish that a live `agy`
accepts this agent, advertises its four tools or leaves the agents directory
otherwise untouched.
