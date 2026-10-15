# 35. Repository tooling is Go, with one-line Makefile recipes

**Decided** (#1000, at the maintainer's request on 2026-10-14, after the
series #993–#999 and #1002–#1004): repository tooling is written in Go, with
its own Go tests, and `Makefile` recipes are one-line calls with no shell
logic. Checks, generators and build helpers live under `tools/`, its own
module, so their dependencies (linters, `govulncheck`) stay out of the
application's `go.mod`; development commands live under `cmd/` in the
application module, whether they import its packages (`cmd/devdb`,
`cmd/capacity`) or not (`cmd/dev`, `cmd/loadgen`; see
[packages](../architecture/packages.md)). This is wider than #1000's
wording ("`cmd/` when it needs the application's packages") on purpose, to
match the commands already there: a new tool goes under `tools/` if it checks
or generates something and under `cmd/` if a developer runs it. The series moved
the dependency graph (`tools/deps`), the prepush fingerprint
(`tools/unchanged`), the formatting and `doc.go` checks (`tools/formatcheck`),
the lint fixtures (`tools/lintfixtures`), the icon sprite
(`tools/buildicons`), the tool downloads (`tools/download`), the worktree
environment (`cmd/devdb`), the capacity runner (`cmd/capacity`) and
`make dev` (`cmd/dev`) to Go; no Python remains.
**Exceptions:** Browser JavaScript in `web/static/` is application code, not
tooling. `db-health` keeps a one-line shell test of two environment
variables to decide whether to call `ai-health`: a guard on whether a tool
runs, not logic of its own; it moves into `cmd/devdb` if it grows.
**Why:** one language for the product and its tooling: the tools share the
module's style, linters and `go test`, the race detector and CI's test jobs
cover them, and the Go/Python duplicate of the test pool limit is gone
(`pgtest.MaxConns`). Shell in recipes had hidden failures before (#848), and
the moves found real defects the old scripts shared — staged changes and
symlinks in the fingerprint, an unreadable token dropping a live database.
**Considered:** keeping Python for the worktree tooling (a second runtime
for contributors and CI, with its own unlinted tests); keeping logic in the
`Makefile` (untested shell, failures hidden by pipelines).
