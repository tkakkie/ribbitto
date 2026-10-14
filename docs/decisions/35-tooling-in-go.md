# 35. Repository tooling is Go, with one-line Makefile recipes

**Decided** (#1000, at the maintainer's request on 2026-10-14, after the
series #993–#999 and #1002–#1004): repository tooling is written in Go — a
command under `tools/`, or under `cmd/` when it needs the application's
packages (as `cmd/devdb` and `cmd/capacity` do) — with its own Go tests, and
`Makefile` recipes are one-line calls with no shell logic. The series moved
the dependency graph (`tools/deps`), the prepush fingerprint
(`tools/unchanged`), the formatting and `doc.go` checks (`tools/formatcheck`),
the lint fixtures (`tools/lintfixtures`), the icon sprite
(`tools/buildicons`), the tool downloads (`tools/download`), the worktree
environment (`cmd/devdb`), the capacity runner (`cmd/capacity`) and
`make dev` (`cmd/dev`) to Go; no Python remains.
**Exceptions:** the AI review launchers in `scripts/ai/` stay in bash: they
are the security boundary of the review workflow (`docs/workflow/running-grok.md`),
run daily, and carry about 1,400 lines of bash tests, so moving them needs its
own decision. Browser JavaScript in `web/static/` is application code, not
tooling.
**Why:** one language for the product and its tooling: the tools share the
module's style, linters and `go test`, the race detector and CI's test jobs
cover them, and the Go/Python duplicate of the test pool limit is gone
(`pgtest.MaxConns`). Shell in recipes had hidden failures before (#848), and
the moves found real defects the old scripts shared — staged changes and
symlinks in the fingerprint, an unreadable token dropping a live database.
**Considered:** keeping Python for the worktree tooling (a second runtime
for contributors and CI, with its own unlinted tests); keeping logic in the
`Makefile` (untested shell, failures hidden by pipelines).
