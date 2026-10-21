# Argon verify operation

The [verify permission](argon-verify.md#status) is in force since
2026-10-21. Production runs use zsh scripts until
[#1436](https://github.com/tkakkie/ribbitto/issues/1436) Stage 2.
After reading #1433's record, the maintainer approved enabling in chat
that day. Evidence: [step 1][step1], [preflight][preflight],
[enabling and passing smoke test][smoke]. Stage 2 is independent of enabling.

## Switch and run

The trusted `verify-mode` file in the maintainer's harness, outside any
repository, holds `on` or `off`. Any other value stops the run.

The complete config-generator invocation is:

```text
implement-config --mode off|on --binary <abs> --guard-dir <abs> [--trusted-source <abs> --worktree <abs>] --out <dir>
```

Both modes require `--mode`, `--binary`, `--guard-dir` and `--out`.
On mode requires `--trusted-source` and `--worktree`; off mode forbids them.
All paths, including `--out`, must be absolute and clean.
The output contains `settings.json`, `hooks.json` and `mcp_config.json`.
`--out` must be absent or an existing empty real directory.

In `on` mode:

1. `run.zsh` asks the trusted binary's `argonreview implement-config --mode on`
   to generate the run's settings, hook (with `--allow-verify`) and MCP file
   for that run's worktree, then installs them in the dedicated home.
   It refuses a guard override.
2. Before the run, `checks.zsh` regenerates and compares those files,
   pins the generated settings and hook by hash, and requires the pinned
   trusted source commit to be clean.
3. After the run, `checks.zsh` regenerates and compares them again, on the
   files agy ran with, before anything is restored. It checks the pins and
   clean trusted source again.
4. The home returns to feature-off settings, hook and empty MCP file;
   an idle check confirms it. At rest the home never holds the permission.

On-mode MCP admits only the exact `verify` call through agreeing guard and
settings layers. In [preflight][preflight], the guard refused every invalid
call first; the settings and server layers were not probed separately.
Arbitrary shell and `run_command` remain refused. Database and
browser tests stay CI-only. `verified` never replaces CI, review or handoff
checks. The Start gate and review independence stay.

## Switch off and problems

Write `off` to `verify-mode`; the scripts then behave as before.
The previous scripts are kept as `run.zsh.verify-off` and
`checks.zsh.verify-off`.

If a run shows a problem, switch off with `verify-mode`, restore and check
the old configuration, and investigate before another attempt. For
leftovers, follow the [sweep procedure](argon-verify-sweep.md).

The [smoke test][smoke] passed: `verify` verified a correct package;
`run_command` plainly and through a shell, an unlisted MCP tool, an extra
argument, `./pkg -v` and another MCP server were refused or unavailable.
Files matched and shared settings were unchanged.

[step1]: https://github.com/tkakkie/ribbitto/issues/1433#issuecomment-6303761064
[preflight]: https://github.com/tkakkie/ribbitto/issues/1433#issuecomment-6304130212
[smoke]: https://github.com/tkakkie/ribbitto/issues/1433#issuecomment-6304812132
