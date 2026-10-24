# Operating two implementer workers

## Status

Two Argon implementer workers run in production since 2026-10-24 by the
[maintainer's decision](https://github.com/tkakkie/ribbitto/issues/1577#issuecomment-6383193096).
Two real runs in parallel, in different worktrees and packages, both counted.
The [design rules](argon-workers.md) still apply: peer homes, worktree lock,
recovery markers, builtin trust and hooks are unchanged.

## Launcher and homes

The pinned launcher is `argonreview-7f607ca`, SHA-256
`11800ec70f4d47e3c89cb57421b4b9d696a13da18c51de9c3261d6bf317cbe51`,
built from the frozen read-only clone `argon-trusted/src-7f607ca`
(origin/main 7f607ca). It contains the peer-home and worktree-lock checks
(#1636). The wrappers pin the binary hash and trusted source.

Worker 2's home is `agy-worker-home-2`. Each worker's run and check wrappers
hard-code HOME and PEERS: the other worker and the review home. Worker 2
reads worker 1's `verify-mode` and refuses when `settings.json`, the agent
or the builtin list differ from worker 1's.
The maintainer signed in worker 2 and trusted `builtin.agy-1.4.7.txt`;
its 21 entries match worker 1's trusted list.

## Assignment

Before assigning a worktree, Claude lists both workers' `guard/` for
`implement-run.recovery-needed`. Follow the design's
[recovery and attribution rules](argon-workers.md#host-wrapper-contract).
Each worktree, `<repo-root>/wt/<name>`, must be a direct child of `<repo-root>/wt/`,
with its own branch and saved Git file. Never reuse a worktree between live runs.

Claude's assignment practice, not a launcher rule: two runs at once only on
different packages and files.

## Rollback

To disable worker 2, first give its implementer directory write permission
with `chmod u+w`, then remove the execute bit of
`agy-worker-home-2/implementer/run.zsh`. This does not stop an active session;
follow [operation and rollback](argon-workers.md#operation-and-rollback)
for stop, recovery and rollback checks.

Worker 1's rollback set was saved before the switch:

| Backup | SHA-256 prefix at the switch (8 hex digits) |
|---|---|
| `hooks.c00020d.json` | `62c61dfc…` |
| `run.zsh.c00020d` | `a14b49db…` |
| `checks.zsh.c00020d` | `1ea67f5e…` |

The full hashes are in the maintainer's record on the host.

If the new launcher binary must also be removed, first disable worker 2,
then stop and recover both workers. Never run mixed launchers: `c00020d`
has no `--peer-home` or worktree lock, so rolled-back worker 1 beside a running
worker 2 has no peer or worktree-lock protection.

Restore this `*.c00020d` set to roll worker 1 back;
`guard/argonreview-c00020d` is still in place. Hashes were recorded, but a
rollback drill with a real run was NOT done, by the maintainer's wish for
speed. Do not treat the saved set as a completed rollback drill.

## Updates and capacity

agy updates apply to both workers; each worker's builtin trust is a separate
maintainer decision, even when the lists match. Launcher changes also need
the maintainer's approval.

Host experiments E0 to E5 were not required and were not run. E1, the quota
experiment, was never started: two parallel runs were enough. The quota
question stays open; a quota problem means going back to one worker.

The three-peer cap counts the review home, so the design allows at most
three workers. A third worker needs the maintainer's approval; never drop
the review home to fit another worker.
