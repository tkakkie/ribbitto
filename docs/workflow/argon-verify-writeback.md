# Argon verify write-back

`argonreview container-generate <worktree>` is **not in force until #1700's
second stage (B)**. Claude uses the trusted binary
[after handoff checks](argon-verify-operation.md#write-back-at-handoff), with no
run process alive. No MCP/verify/guard access;
`generated` stays report-only.

Before sweeping, refuse non-absolute, unclean or non-real worktree paths,
any symlink (the guard's exact walk, including ordinary `.git` directories),
or `sqlc.yaml` absent/different from its merge-base blob with `origin/main`.
Hardened, bounded Git supports SHA-1/SHA-256; errors refuse.

Reuse copy, image, flags and limits. The fixed script copies to tmpfs, then
runs templ/sqlc/Tailwind (statuses 31/32/33).
GNU find passes names to `cmp -s`; a NUL list feeds `tar --format=ustar`
(selection errors: 34); an empty list emits 1024 zero bytes without tar.
Assume GNU find/cmp/tar from the pinned Debian Go base; unsupported names fail.
No PAX.
Parse stdout only; logs go to stderr. The shared
stdout/stderr 4 MiB budget, timeout, nonzero exit, runner error or unconfirmed
container/source cleanup refuses before writes.

Refuse unsafe archives; caps: 200 entries, 1 MiB each, 4 MiB total.
Require the generator banner and one rule:

- Direct `N_templ.go` in `internal/web/view` or `internal/lintfixture`, with
  regular sibling `N.templ`.
- `internal/<M>/internal/postgres/sqlcgen/`: only `db.go`, `models.go`, or
  `Q.sql.go` with regular `db/queries/<M>/<Q>.sql`. The queries directory must
  exist and the module's sqlcgen `doc.go` must be a regular blob at the base.
- Exactly `web/static/css/app.css`.

Validate every fact/destination before writes. Root/parent opens refuse
symlinks; parents must exist. Destinations must be regular, one-link files
owned by the running user, read via capped nonblocking/no-follow descriptors. Only templ and sqlc query output may be absent; `db.go`,
`models.go` and CSS must exist. Ignore tar modes.

Prepare **all** differing files as random exclusive temps in their held parent
directories, explicitly chmod 0644, write and fsync **before any rename**.
Preparation failure replaces nothing. Recheck root/parent and
destination identity (device, inode, mode, links, owner, size, mtime, ctime),
or continued absence. Atomicity is per file, **not per set**: later identity
or rename failure leaves earlier replacements in place. Remove unused temps
through the same descriptors; cleanup failures name file/temp. Parent fsync
is best effort; failure reports nonzero. Retries skip identical files untouched; nothing is deleted.

After cleanup, stdout reports escaped replaced paths, SHA-256 before (`absent`
for new files)/after, written/identical counts, refusals and paths not replaced. Names/diagnostics are escaped and bounded.
Empty tar succeeds: `0 written, 0 identical`. Exit 0 requires every file
written/equal. Stale outputs remain for review.
A budget refusal exits 1; the report's first line is `NOT VERIFIED: verification budget ...` instead of `N written, M identical`.
