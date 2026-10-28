# Verify image recipe

## Purpose and status

The recipe, build/vet/test regeneration and `generated` (#1696), the
[result summary](argon-verify-testdb.md#result-summary) and
[test-db](argon-verify-testdb.md) (#1697) are in force since 2026-10-27
(#1700 stage A). Both workers' enabled launcher `argonreview-2e3a6bf` still
pins `sha256:12c5636332bebbfbc6f734bac9c7bb59b3bdd9ac9e4ed373beb180fca8e79e80`
(tag `argonreview-verify-image:7957ae8`, #1697 record); A2 leaves it unaffected.
Launcher `argonreview-7f607ca` and official golang image stay for rollback.
PostgreSQL 18 (#1697 PR A) and Chromium are built and recorded; A2 pins the
new image in main's source below, not in force. Nothing here enables anything.
The recipe `cmd/argonreview/verifyimage/Dockerfile` is the path and kind in
[decision 42](../decisions/42-repository-languages-and-formats.md#language-list).
The [boundary](argon-verify.md#verification-container) is the trusted runner's
flags and pinned image, not the recipe or its test.

## Recipe rules

- Every base uses the SHA-256 digest in `containerBaseImage`;
  no parser directives, including `# syntax=`.
- Tailwind's exact version matches `tailwindVersion` in
  `tools/download/main.go`. Its Linux arm64 binary is verified with a
  literal SHA-256 and `sha256sum -c` in the same RUN as the download.
  The RUN checks arm64 first; amd64 is out of scope, untested.
- COPY takes only `tools/go.mod` and `tools/go.sum`. templ and sqlc are
  built against those manifests with inline `GOFLAGS=-mod=readonly` and
  `CGO_ENABLED=1`; modules are verified against the committed checksums.
  sqlc generation is very slow without build-time cgo. Remove the build
  directory, module and build caches in that RUN.
- Only FROM, RUN and COPY are allowed. No ADD, VOLUME, ENTRYPOINT, CMD,
  USER, EXPOSE, HEALTHCHECK, ONBUILD, ARG, ENV or SHELL; no RUN flags
  (including secret mounts), plain `apt`.
- Debian pins include `postgresql-common=278` and
  `ca-certificates=20250419+deb13u1`; PGDG supplies
  `postgresql-18=18.6-1.pgdg13+2`. Use `apt-get update` and
  `apt-get install -y --no-install-recommends` only, then remove apt lists.
  Set and verify `create_main_cluster = false` before installing PostgreSQL
  (no unused cluster). `useradd` creates `check`, uid 1000, home `/home/check`:
  `initdb` needs a user entry for its uid.
- Before adding PGDG with the bare command
  `/usr/share/postgresql-common/pgdg/apt.postgresql.org.sh -y`, check
  fingerprint `B97B0AFCAA1A47F044F244A07FCC7D46ACCC4CF8` against Debian's
  dearmored `/usr/share/postgresql-common/pgdg/apt.postgresql.org.gpg`,
  the keyring named by the generated source's `Signed-By`. The check is the
  last statement of an earlier RUN that starts with errexit, never disables it
  and has no `||`.
  Require exactly one fingerprint line: any other key fails the build.
- Decision 42 says apt packages use exact versions. This recipe pins only
  the five packages it names; dependencies such as `postgresql-client-18`
  and `libpq5` float. The maintainer confirmed this reading of decision 42 on 2026-10-26
  (#1697). Rebuilds do not guarantee the same bytes; the host record's
  image ID identifies the build. If `postgresql-common` or `postgresql-18`
  leaves its repository, the build fails until a pull request updates
  the pin. The `ca-certificates` pin matches the base image's installed version
  and is a no-op at this digest; only a base-image bump makes it fail as a
  refused downgrade.

`TestVerifyImageRecipe` checks the base, instruction and COPY restrictions,
download version and same-RUN checksum, parser directives, architecture
check, bare apt-get commands, pinned allowed packages with `-y` and
`--no-install-recommends`, the PGDG setup command, the approved fingerprint
(also required in this page; a key change needs the test constant, this page
and a review) and the cluster edit with its grep in an errexit RUN.
Tier A review also checks tool builds, user and cache removal.
The amendment covers Chromium and fonts-liberation ([updates](argon-verify-browser-updates.md));
another package set or second recipe still needs maintainer-approved amendment.

## Runner pin and prelude

Main's `containerVerifyImage` pins `.Id` from `docker image inspect`:
`sha256:bdd988255da2296494d1d4dcb2e944702c082480482aa5b8777fceadf8fc1ef1`
(tag `argonreview-verify-image:91dfaa7`, [#1908 build record][build]; old history: #1697).
With containerd this is the manifest-list digest; the pin is `.Id` on any store.
Runs use `--pull=never`; both sweep inspections require that same ID.
Effective only after integrated preflight and separate maintainer approval
to install a launcher built from a commit including A2. For #1700/#1908,
the old launcher's `container-sweep` must find no leftover before switching:
old-image containers fail every check closed; the new sweep cannot reclaim
them ([preflight item 3](argon-verify-browser-preflight.md#provisioning-before-enabling)).
The recipe test checks the base pin. Before Docker, the copied root manifest
must name Go 1.27.3; `tools/go.mod` must require
templ v0.3.1070 and sqlc v1.31.1 exactly once each. Missing, duplicate or
different tool requirements refuse the run: the verify image needs a rebuild.
Quoted syntax or unsupported parentheses (including malformed blocks) refuse.

Build, vet and test use one fixed `sh` prelude with `set -eu`. It copies
`/work/.` (including dotfiles) to `/tmp/work` on tmpfs, then runs the installed
templ, sqlc and Tailwind generators from that repository root, using
`sqlc.yaml` and the Makefile's CSS arguments. Generator stdout goes to stderr;
test stdout remains test2json only. Any generator failure exits nonzero and
reports `FAILED`. The script ends with `exec go "$@"`; the check, fixed flags
and package pattern are separate positional arguments. `/work` stays read-only.
Fresh tmpfs per run makes a fixed directory sufficient.
1 GiB tmpfs limits stay; #1700's preflight ran the limits and three simultaneous containers.

Reports say sources were regenerated; `generated` checks committed output
is current. CI remains the check; race, cgo and database/browser limits remain.
sqlc configuration can invoke process plugins inside the container, where
tests can already execute code.

## Host build procedure

After recipe merge, Claude builds from the frozen trusted clone at that
merge commit. **The maintainer approves the build in chat first**, including
the approximately 200 MB download. Network access is at build time only.

Create a fresh context with exactly these three files from that commit:

```text
<context>/Dockerfile
<context>/tools/go.mod
<context>/tools/go.sum
```

COPY paths are relative to this context, not the repository or recipe directory. Run:

```sh
docker build --no-cache -t argonreview-verify-image:<commit> -f <context>/Dockerfile <context>
```

Record on the rebuild issue (#1697 history, #1908 browser): full commit,
SHA-256 of all three context
files, build log tail, image ID and size from
`docker image inspect argonreview-verify-image:<commit>`, `go version`,
`templ version`, `sqlc version`, and the Tailwind banner. For #1697 also
record `postgres --version` (using `/usr/lib/postgresql/18/bin/postgres`)
and the installed package list with versions (`dpkg-query -W`), confirming
`postgresql-common` is still 278 after the PGDG install.
Also record SHA-256 of the three installed binaries:
`/usr/local/bin/tailwindcss`, `/usr/local/bin/templ` and
`/usr/local/bin/sqlc`. The record, not the recipe alone, identifies the built image.

## Rebuilds

Rebuild for Tailwind, templ, sqlc or base-image changes, or a Debian security
fix the maintainer wants. PostgreSQL's addition required a host rebuild and
pin pull request (#1697). Each rebuild changes the image ID, needs the same
provenance record and a new tier A pull request updating the runner pin;
the host build still needs the maintainer's chat approval.
A templ or sqlc version bump in `tools/go.mod` refuses every verify run until
`containerTemplVersion`/`containerSQLCVersion` and the image pin change together
in the same pull request.

## Generated check

`generated` refuses packages other than `./...` before Docker. Its separate
fixed script takes no caller arguments.
Shared copy/generator constants preserve build/vet/test's prelude.
After copying and the three generators, it runs `templ fmt -fail .`, captures
`gofmt -l .`, then compares `diff -ru /work /tmp/work`. Generator and formatter
diagnostics go to stderr; stdout carries gofmt names or the diff.
Writes stay in the container copy: report only, no write-back to the worktree.

| Status | Result |
|---|---|
| 0, empty stdout, no runner error/cap | `verified` |
| 21 | `FAILED: generation failed` |
| 22 (templ), 23 (gofmt names) | `FAILED: formatting` |
| 25 | `FAILED: generated files differ` |
| 24 (gofmt error), 26 (diff error), other | `FAILED`, with status |

[Timeouts](argon-verify-testdb.md#timeout-and-cancel-results) never verify.
Output cap: `NOT VERIFIED: output limit exceeded (output cut off)`.
Status 0 with stdout or a bad status-25 diff never verifies.

Trusted Go validates complete GNU unified hunks and computes the stat:
path and added/removed counts, new/only-in-worktree entries, or binary entries.
It lists at most 100 paths (each at most 200 bytes), then path/line totals
and omitted-path count. Malformed streams lose the whole stat and report
that the diff was not understood.
The host stat precedes scope, stdout and stderr; its diff excerpt is capped
at 16 KiB plus a cut notice. MCP's summary is capped at 7,000 bytes,
with generated totals before paths. Host diff lines over 400
bytes are cut at rune boundaries with a visible marker. Gofmt lists at most
50 names, each at most 200 bytes, then the omitted count.

CI runs `make generate` and `make css` in a full checkout, refusing any
`git status --porcelain` output, including untracked files. The container
compares only the selected source copy: Git metadata, protected names and
ignored untracked files are absent. A newly generated file appears as
`Only in /tmp/work`; a new directory is one entry, since recursive diff does
not descend into directories present on only one side. The image pins
Tailwind; the runner does not detect Tailwind version drift in the copy.

Formatting runs in this check. CI's `check-static` runs `tools/formatcheck`:
it lists files except root `bin/`, compares templ stdout without rewriting
and checks internal `doc.go` presence. This check's recursive formatters can
traverse differently (including hidden directories) and omit package
documentation. tbls schema docs, dependency docs, the API
manifest, golangci-lint, docscheck, languagecheck, race detector, browser tests
and database tests other than [test-db](argon-verify-testdb.md) remain CI-only.
#1700's preflight exercised real Docker/dash/generators before these checks entered force.

[build]: https://github.com/tkakkie/ribbitto/issues/1908#issuecomment-6462194722
