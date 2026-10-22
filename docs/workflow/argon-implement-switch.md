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

The wrappers check the SHA-256 of the pinned `guard/argonreview-c00020d`
before the run; `run.zsh` also checks it afterwards. They pass the frozen
clone `argon-trusted/src-c00020d` as `RIBBITTO_ARGON_SOURCE` and, in on mode,
as `--trusted-source`.

## Recovery and rollback

A started run that does not count leaves `guard/implement-run.recovery-needed`
(a run refused before it starts, for example by the at-rest check, leaves none).
The next run is refused until the ordered
[Manual recovery](argon-implement-run-recovery.md#manual-recovery) is done.

The rollback set is kept in the worker home: `hooks.97d19a2.json`,
`checks.zsh.97d19a2` and `run.zsh.97d19a2`. Their SHA-256 hashes were
recorded before the switch. To roll back, restore them as `hooks.json`,
`checks.zsh` and `run.zsh`, respectively, and copy `hooks.json` to
`.gemini/config/hooks.json`; `guard/argonreview-97d19a2` stays in place for the old
hook. Then run the old `checks.zsh <worktree> <saved .git file> idle`. This rollback was drilled with a real run on
2026-10-22. The set supersedes `run.zsh.verify-off` and `checks.zsh.verify-off`.

No Python is in the run path. `guard/implementer-guard.py` and
`hooks.python.json` are reference only.

[comparison]: https://github.com/tkakkie/ribbitto/issues/1436#issuecomment-6320069337
[switch]: https://github.com/tkakkie/ribbitto/issues/1436#issuecomment-6322877156
