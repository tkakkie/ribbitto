# The Go runner switch (2026-10-22)

On 2026-10-22, production switched the Argon implementer from the zsh
implementations to the Go runner under
[#1436](https://github.com/tkakkie/ribbitto/issues/1436).
Evidence: [runner comparison][comparison] and [switch record][switch].

## Wrappers and trusted inputs

In the maintainer's worker home (`<worker-home>`, the dedicated Antigravity
home of the [boundary](argon-implementer.md#the-boundary)),
`<worker-home>/implementer/run.zsh` and `checks.zsh` are thin wrappers around `argonreview implement-run` and
`argonreview implement-check`, respectively. Usage:

```text
run.zsh <worktree> <saved .git file> <prompt-file> <logdir> [timeout]
checks.zsh <worktree> <saved .git file> [idle]
```

Both derive `--verify` from the trusted `verify-mode` file (`checks.zsh idle`
always uses `off`, the state at rest) and set `LANG=en_US.UTF-8`. The mode is `on` as of 2026-10-22. To switch verify
off, write `off` to `verify-mode`; the Go commands accept `--verify off|on`.
See [verify operation](argon-verify-operation.md) for the run sequence.

The wrappers check the SHA-256 of the pinned `guard/argonreview-2e3a6bf`
(since 2026-10-27) before the run; `run.zsh` also checks it afterwards. They pass the frozen
clone `argon-trusted/src-2e3a6bf` as `RIBBITTO_ARGON_SOURCE` and, in on mode,
as `--trusted-source`. Worker 2 and the launcher updates are in
[worker operation](argon-workers-operation.md).

The optional `--browser-mode <abs path>` is available on `implement-run`,
`implement-check`, `implement-config`, `verify-mcp` and `container-check`.
The browser admission checks and host-only `browser-check-record` command are
merged but not in force; the browser runner is unwired. An absent flag means
paused. The implementer commands require verify on (config: mode on).

## Recovery and rollback

A started run that does not count leaves `guard/implement-run.recovery-needed`
(a run refused before it starts, for example by the at-rest check, leaves none).
The next run is refused until the ordered
[Manual recovery](argon-implement-run-recovery.md#manual-recovery) is done.

The current rollback set is kept in worker 1's home: `hooks.c00020d.json`,
`checks.zsh.c00020d` and `run.zsh.c00020d`, with
`guard/argonreview-c00020d` still in place. See
[worker rollback](argon-workers-operation.md#rollback) for the recorded hashes.
To roll back, restore the set as `hooks.json`, `checks.zsh` and `run.zsh`,
respectively, and copy `hooks.json` to `.gemini/config/hooks.json`. Then run
the old `checks.zsh <worktree> <saved .git file> idle` and one real run under
the maintainer's approval. The 2026-10-24 set was NOT drilled with a real run.

The older set, `hooks.97d19a2.json`, `checks.zsh.97d19a2` and
`run.zsh.97d19a2`, had its SHA-256 hashes recorded before the 2026-10-22
switch, with `guard/argonreview-97d19a2` kept for its hook. That rollback was
drilled with a real run on 2026-10-22 and superseded `run.zsh.verify-off` and
`checks.zsh.verify-off`. Do not mix backup sets.

No Python is in the run path. `guard/implementer-guard.py` and
`hooks.python.json` are reference only.

[comparison]: https://github.com/tkakkie/ribbitto/issues/1436#issuecomment-6320069337
[switch]: https://github.com/tkakkie/ribbitto/issues/1436#issuecomment-6322877156
