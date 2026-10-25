# Verify image recipe

## Purpose and status

The recipe, build/vet/test regeneration and `generated` check (#1696) are merged but
**not in force until #1700**.
Production workers still run the pinned launcher `argonreview-7f607ca`
and the old image; merging changes nothing in production.
The recipe is `cmd/argonreview/verifyimage/Dockerfile`,
the exact path and kind admitted by
[decision 42](../decisions/42-repository-languages-and-formats.md#language-list).
The [verification boundary](argon-verify.md#verification-container) remains
the trusted runner's flags and pinned image, not this recipe or its test.

## Recipe rules

- Every base uses the SHA-256 digest in `containerBaseImage`;
  no parser directives, including `# syntax=`.
- Tailwind's exact version matches `tailwindVersion` in
  `tools/download/main.go`. Its Linux arm64 binary is verified with a
  literal SHA-256 and `sha256sum -c` in the same RUN as the download.
  The RUN checks arm64 first; amd64 is out of scope and untested.
- COPY takes only `tools/go.mod` and `tools/go.sum`. templ and sqlc are
  built against those manifests with inline `GOFLAGS=-mod=readonly` and
  `CGO_ENABLED=1`; modules are verified against the committed checksums.
  sqlc generation is very slow without cgo at build time. Remove the build
  directory, module cache and build cache in that RUN.
- Only FROM, RUN and COPY are allowed. No ADD, VOLUME, ENTRYPOINT, CMD,
  USER, EXPOSE, HEALTHCHECK, ONBUILD, ARG, ENV or SHELL; no RUN flags
  (including secret mounts), apt, added user, browser or PostgreSQL today.

`TestVerifyImageRecipe` checks the base, instruction and COPY restrictions,
download version and same-RUN checksum, parser directives and architecture
check. Tier A review also checks the tool builds and cache removal.
Only generators are included now. PostgreSQL 18 may be added later under
#1697; a browser, another package set or a second recipe needs a decision 42
amendment approved by the maintainer.

## Runner pin and prelude

`containerVerifyImage` pins the image ID recorded on #1696:
`sha256:acb981ac1cf26d383cfe4a4791d5ca5ecd4507e594992585230b0c7d7e4768d1`.
Runs use `--pull=never`; both sweep inspections require that same ID.
The recipe test separately checks the base pin. Before Docker starts, the
copied root manifest must name Go 1.27.3, and `tools/go.mod` must require
templ v0.3.1070 and sqlc v1.31.1 exactly once each. Missing, duplicate or
different tool requirements refuse the run: the verify image needs a rebuild.
Quoted syntax or unsupported parentheses (including malformed blocks) also refuse.

Build, vet and test use one fixed `sh` prelude with `set -eu`. It copies
`/work/.` (including dotfiles) to `/tmp/work` on tmpfs, then runs the installed
templ, sqlc and Tailwind generators from that repository root, using
`sqlc.yaml` and the Makefile's CSS arguments. Generator stdout goes to stderr;
test stdout remains test2json only. Any generator failure exits nonzero and
reports `FAILED`. The script ends with `exec go "$@"`; the check, fixed flags
and package pattern are separate positional arguments. `/work` stays read-only.
The container is single-run with a fresh tmpfs, so a fixed directory suffices.
Existing 1 GiB tmpfs limits stay; real Docker capacity validation is for #1700.

Reports say sources were regenerated; the `generated` check covers whether
committed generated files are current. CI remains the check.
Race, cgo and database/browser limits remain. sqlc configuration can invoke
process plugins inside the container, as tests can already execute code there.

## Host build procedure

After the recipe is merged, Claude performs the host step from the frozen
trusted clone at that merge commit. **The maintainer approves the build in
chat first**, including the approximately 200 MB download. This is a build
with network access at build time only.

Create a fresh context directory containing exactly these three files,
copied from that commit, with no other files:

```text
<context>/Dockerfile
<context>/tools/go.mod
<context>/tools/go.sum
```

COPY paths are relative to this context, not the repository or the recipe's
original directory. Run:

```sh
docker build --no-cache -t argonreview-verify-image:<commit> -f <context>/Dockerfile <context>
```

Record provenance on #1696: the full commit, SHA-256 of all three context
files, build log tail, image ID and size from
`docker image inspect argonreview-verify-image:<commit>`, `go version`,
`templ version`, `sqlc version`, and the Tailwind banner.
Also record SHA-256 of the three installed binaries:
`/usr/local/bin/tailwindcss`, `/usr/local/bin/templ` and
`/usr/local/bin/sqlc`. This record identifies the actual built image;
the recipe alone does not establish its image ID.

## Rebuilds

Rebuild for a Tailwind, templ, sqlc or base-image change, or a Debian security
fix the maintainer wants. Each rebuild changes the image ID, needs the same
provenance record and a new tier A pull request updating the runner pin;
the host build still needs the maintainer's chat approval.
A templ or sqlc version bump in `tools/go.mod` refuses every verify run until
`containerTemplVersion`/`containerSQLCVersion` and the image pin change together
in the same pull request.

## Generated check

`generated` requires package exactly `./...`; other patterns are refused
before Docker. It has a separate fixed script with no caller arguments.
Shared copy and generator command constants preserve build/vet/test's prelude.
After copying and the three generators, it runs `templ fmt -fail .`, captures
`gofmt -l .`, then compares `diff -ru /work /tmp/work`. Generator and formatter
diagnostics go to stderr; stdout carries gofmt names or the diff.
All writes stay in the container copy: report only, no write-back, nothing
written to the worktree.

| Status | Result |
|---|---|
| 0, empty stdout, no runner error/cap | `verified` |
| 21 | `FAILED: generation failed` |
| 22 (templ), 23 (gofmt names) | `FAILED: formatting` |
| 25 | `FAILED: generated files differ` |
| 24 (gofmt error), 26 (diff error), other | `FAILED`, with status |

A timeout or runner error fails; the output cap overrides with
`NOT VERIFIED: output limit exceeded (output cut off)`. Status 0 with stdout
also fails. A missing or unparseable diff at status 25 never verifies.

Trusted Go code validates complete GNU unified hunks and computes the stat:
path and added/removed counts, new/only-in-worktree entries, or binary entries.
It lists at most 100 paths (each path text at most 200 bytes), then the count
and line totals over all paths, with an omitted-path count. Malformed streams
lose the whole stat and report that the diff was not understood.
The stat precedes scope, stdout and stderr, so the MCP 48 KiB tail cap keeps
it. The diff excerpt has a 16 KiB budget plus a cut notice; lines over 400
bytes are cut at rune boundaries with a visible marker. Gofmt lists at most
50 names, each at most 200 bytes, then the omitted count.

CI runs `make generate` and `make css` in a full checkout and refuses any
`git status --porcelain` output, including untracked new files. The container
compares only the selected source copy: Git metadata, protected names and
ignored untracked files are absent. A newly generated file appears as
`Only in /tmp/work`; a new directory is one entry, since recursive diff does
not descend into directories present on only one side. The image pins
Tailwind; the runner does not detect Tailwind version drift in the copy.

Formatting runs in this check; CI runs `tools/formatcheck` in `check-static`.
That tool enumerates files except root `bin/`, compares templ stdout without
rewriting, and also checks internal package `doc.go` presence. Recursive
formatter traversal here can differ (including hidden directories), and does
not check package documentation. tbls schema docs, dependency docs, the API
manifest, golangci-lint, docscheck, languagecheck, race detector, database and
browser tests remain CI-only. The real Docker/dash/generator path still needs
#1700 preflight before these merged checks enter force.

## Future generated-file write-back helpers

`cmd/argonreview/writeback_archive.go` and `cmd/argonreview/writeback_paths.go`
provide a pure archive parser and repository-derived path allowlist for a
future generated-file write-back. They write nothing and have no production
callers. Only USTAR and PAX headers are accepted; part B's producer must use
`tar --format=ustar` (or pax). The write-back command and writer come in a
later pull request (#1699, part B). Nothing is written to the worktree and
this write-back is not in force.
