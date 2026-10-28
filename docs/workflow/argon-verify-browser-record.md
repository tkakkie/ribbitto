# Browser security check record

**Merged but not in force:** the gate and host-only record command are
merged; nothing here enables or authorizes anything. Follow the
[preflight and separate approvals](argon-verify-browser-preflight.md)
before adoption. [Browser operation](argon-verify-browser.md) covers
admission; [weekly checks](argon-verify-browser-updates.md#weekly-check-and-expiry)
establish the completion being recorded.

## Gate file

Only `argonreview browser-check-record` writes
`/var/cache/argonreview/browser-security-check`; it is host-only, never an
Argon, guard or MCP command. The runner never reads tracking issue #1910 or
fetches the record URL. It reads the gate on every `test-browser` call
after the switch and before reservation or copy.

The parent follows [host lock provisioning](argon-verify-budget.md#host-lock-provisioning):
real, uid-owned, not group- or world-writable. The gate is a regular,
uid-owned, one-link file, exactly 0600 with no special bits; symlinks are
refused. Its sole format is three newline-terminated lines in this order,
at most 2,048 bytes:

```text
switch=<absolute trusted browser-mode path>
completed=<canonical RFC 3339 UTC time ending in Z>
record=<repository issue or issue-comment URL>
```

Time uses canonical RFC 3339, including canonical fractional seconds if
present; a UTC offset spelling instead of `Z` is refused. The URL is
`https://github.com/tkakkie/ribbitto/issues/<digits>`, optionally followed
by `#issuecomment-<digits>`; each number has 1–20 ASCII digits, with no
query or trailing suffix. The switch path follows the
[path rules](argon-verify-browser.md#switch-and-admission-order).

A missing, malformed, unsafe or future entry, an entry **14 days old or
more**, or a switch path differing from the caller's `--browser-mode`
gives `NOT VERIFIED: browser check paused: security check overdue`.
Exactly 14 days is expired; there is no grace period.

The same gate directory holds `browser-security-check.lock`. Record writers
serialize through this sibling, with the lock-file identity rules above,
waiting at most ten seconds. Publication is atomic; runners read without
locking or creating files. The writer checks time after acquiring its lock
and reading the previous entry, so waiting cannot bypass expiry.

## Initial and routine recording

Claude first completes tracking issue #1910's weekly record. Initialize only
through a frozen wrapper in worker 1's `implementer/` (0500). It hard-codes
worker 1's switch path and pins the binary hash as `run.zsh` does for
`verify-mode`. The command form used by that wrapper is:

```text
argonreview browser-check-record --init --browser-mode <abs path> --completed-at <RFC3339 UTC Z> --record <issue URL>
```

`--init` requires a regular uid-owned switch file in a real uid-owned
directory of exactly 0500, also containing a regular `verify-mode` (no
symlink). These are location checks; the command checks neither switch
content nor mode 0400. Before `--init`, Claude or the host-side frozen
wrapper with its pinned binary hash must confirm that the switch reads
`off` and is mode 0400: both are operator rules. It never overwrites an
existing gate,
including a symlink. Changing the recorded path requires the maintainer's
approval to remove the gate first.

After **each complete weekly record**, Claude uses:

```text
argonreview browser-check-record --completed-at <RFC3339 UTC Z> --record <issue URL>
```

Routine recording takes no switch-path argument: it reads that path from
the trusted gate. A future or older completion time is refused; equality
with the previous time is accepted. An expired entry cannot be refreshed
while its switch reads `on`. Write `off` first; a fresh record never
supplies the maintainer's re-enabling approval. The command can record an
already old completion, but that does not make the runner gate fresh.

Exit 0 prints `browser-check-record: recorded`; usage/field errors exit 2,
recording errors exit 1. Exit 1 also covers failure to write the success
line after publishing the entry; re-read the gate before retrying. Success
records supplied data, not proof of completeness or source fetches.

## Refusals

These are exact fixed diagnostics on stderr; refusal has no success line:

```text
usage: argonreview browser-check-record [--init --browser-mode <abs path>] --completed-at <RFC3339 UTC Z> --record <issue URL>
browser-check-record: invalid completion, reference or switch path
browser-check-record: initial switch must be a regular owned file
browser-check-record: initial switch directory must be owned and mode 0500
browser-check-record: initial switch directory must contain regular verify-mode
browser-check-record: completion is in the future
browser-check-record: completion is older than the recorded check
browser-check-record: expired check cannot be refreshed while browser-mode is on
browser-check-record: security check exceeds size limit
browser-check-record: security check lock unavailable or unsafe
browser-check-record: security check is being updated
browser-check-record: unsafe security check directory
browser-check-record: unsafe security check file
browser-check-record: malformed security check
browser-check-record: malformed security check fields
```

I/O failures use `browser-check-record: ` followed by the failed operation
and its system error: `opening security check directory`,
`opening security check (run --init if missing)`, `reading security check`,
`naming security check`, `creating security check`, `writing security check`,
`publishing security check`, `removing security check staging link` or
`syncing security check directory`. Existing-gate initialization fails at
publication. Diagnose the named step; do not treat an error as recording
success or as approval to remove or replace trusted state.

## Update costs

Later Debian Chromium fixes repeat the full
[update path](argon-verify-browser-updates.md#update-path), including package
diff review. Every update costs a tier A pin pull request, an approved
host build, a tier A image-ID pull request, a preflight subset and a separately
approved launcher update; this can recur about weekly. The issue's image
estimate is roughly +1 GB (2.82 GB versus 1.77 GB); the build record must
establish the actual size.
