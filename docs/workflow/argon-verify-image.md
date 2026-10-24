# Verify image recipe

## Purpose and status

The recipe is merged as #1696's pull request A, but the image is **not in
use until #1700**. Today's runner still uses the stock pinned Go image in
`containerGoImage`. The file is `cmd/argonreview/verifyimage/Dockerfile`,
the exact path and kind admitted by
[decision 42](../decisions/42-repository-languages-and-formats.md#language-list).
Later work (#1696 PR B, #1700) points the runner at the built image.
The [verification boundary](argon-verify.md#verification-container) remains
the trusted runner's flags and pinned image, not this recipe or its test.

## Recipe rules

- Every base uses a SHA-256 digest equal to the Go-image digest in
  `containerGoImage`; no parser directives, including `# syntax=`.
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
docker build --no-cache -f <context>/Dockerfile <context>
```

Record provenance on #1696: the full commit, SHA-256 of all three context
files, build log tail, image ID from `docker image inspect <image>`, image
size, `go version`, `templ version`, `sqlc version`, and the Tailwind banner.
Also record SHA-256 of the three installed binaries:
`/usr/local/bin/tailwindcss`, `/usr/local/bin/templ` and
`/usr/local/bin/sqlc`. This record identifies the actual built image;
the recipe alone does not establish its image ID.

## Rebuilds

Rebuild for a Tailwind, templ, sqlc or base-image change, or a Debian security
fix the maintainer wants. Each rebuild produces a new image ID and the same
provenance record. A changed recipe goes through a new tier A pull request;
the host build still needs the maintainer's chat approval.
