# Argon review agent

Reviews, issue reviews, Copilot checks and probes launch with
`--agent argon-reviewer`; `--model` still selects `gemini-4-argon-high`.
The custom agent tells `agy` to offer exactly `view_file`, `list_dir`,
`grep_search` and `find_by_name`. It offers no MCP, shell, write, URL,
subagent or schedule tool. The guard is the second layer: its unchanged
allowlist refuses everything else, including `read_file`. Deny rules and
headless mode refuse reads outside the checkout.

## Preparation

`prepare` owns four files beneath the dedicated HOME's `.gemini`:
`antigravity-cli/settings.json`, `config/hooks.json`,
`config/mcp_config.json` and `config/agents/argon-reviewer.md`.
The agent's static bytes come from trusted code, never PR or issue input.
Every review, issue review, Copilot check, probe and `check` starts with
`prepare`, which rewrites and byte-compares all four files on every run.

The agent stays at rest. This is simpler and stricter: one mechanism,
nothing to remove, and no per-run placement window. Every other entry in
`.gemini/config/agents` is refused as customisation, including hidden files,
subdirectories, other agents and case aliases. Owned directories and files
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
- A process running as you can change the dedicated `HOME` between
  `prepare` and launch; it could change your configuration anyway.
- The supervisor stops every process in `agy`'s session; a descendant that
  calls `setsid` leaves it and is not stopped.

See [running Argon](running-argon.md#evidence) for recorded probes and the
pending #1501 host probe. Pre-merge tests do not establish that a live `agy`
accepts this agent, advertises its four tools or leaves the agents directory
otherwise untouched.
