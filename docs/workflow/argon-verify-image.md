# Verify image recipe

## Purpose and status

The recipe, regeneration prelude and image pin (#1696) are merged but
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

Build, vet and test use one fixed `sh` prelude with `set -eu`. It copies
`/work/.` (including dotfiles) to `/tmp/work` on tmpfs, then runs the installed
templ, sqlc and Tailwind generators from that repository root, using
`sqlc.yaml` and the Makefile's CSS arguments. Generator stdout goes to stderr;
test stdout remains test2json only. Any generator failure exits nonzero and
reports `FAILED`. The script ends with `exec go "$@"`; the check, fixed flags
and package pattern are separate positional arguments. `/work` stays read-only.
The container is single-run with a fresh tmpfs, so a fixed directory suffices.
Existing 1 GiB tmpfs limits stay; real Docker capacity validation is for #1700.

Reports say sources were regenerated; CI still checks whether committed
generated files are current. No `generated` check yet (#1696).
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
