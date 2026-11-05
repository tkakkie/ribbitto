# Dependency and runtime update coverage

Update this document only when dependency or runtime update requirements, rationale, operator actions or promises change.

Passing nightly CI (`make check`) and `make vuln` (`govulncheck`) prove that current pins build, pass tests and have no reachable Go vulnerability; they do not show whether runtimes, container digests or downloaded binaries are current.

## Version-pin inventory

| Class | Source of truth | Update detector | Cadence | Owner / next action |
|---|---|---|---|---|
| Application Go module | `go.mod`, `go.sum` | Dependabot (`gomod`, `/`, `templ` group) + CI `make vuln` | Weekly (7-day cooldown); `make vuln` nightly and per PR | Maintainer reviews PR; regenerate if `templ` changes (`make generate`) |
| Tooling Go module and `tool` directives (`templ`, `sqlc`, `govulncheck`, `esbuild`) | `tools/go.mod`, `tools/go.sum` | Dependabot (`gomod`, `/tools`, `templ` group) + CI `make vuln` | Weekly (7-day cooldown); `make vuln` nightly and per PR | Maintainer reviews PR; run `make generate` and `make bundles` |
| Schema-doc Go module (`tbls`) | `tools/tbls/go.mod`, `tools/tbls/go.sum` | Dependabot (`gomod`, `/tools/tbls`) + CI `make vuln` | Weekly (7-day cooldown); `make vuln` nightly and per PR | Maintainer reviews PR; verify `make schema-docs` |
| Go toolchain runtime | `go` directive in `go.mod`, `tools/go.mod`, `tools/tbls/go.mod` | `tools/versioncheck` (`.github/workflows/version-coverage.yml`) + `make vuln` | Monthly schedule (`17 5 1 * *`) and `workflow_dispatch` | Manual/AI PR updating all three `go.mod` files together |
| Node runtime | `.node-version` | `tools/versioncheck` (`.github/workflows/version-coverage.yml`) | Monthly schedule and `workflow_dispatch` | Manual/AI PR updating `.node-version` and regenerating `package-lock.json` ([web toolchain](architecture/web-toolchain.md#versions-and-installation--1410-1415)) |
| npm / TypeScript checker | `package.json`, `package-lock.json` | Dependabot (`npm`, `/`) | Weekly (7-day cooldown) | Maintainer reviews PR keeping exact pin and lockfile in sync via `npm ci --ignore-scripts` |
| PostgreSQL development image | `compose.yml` (`image: postgres:18`) | Dependabot (`docker-compose`, `/`); `tools/versioncheck` only verifies the tag is still `postgres:18` and compares the CI digests with that tag's registry digest | Weekly (7-day cooldown) | Keep on current major tag; major bump requires a separately approved migration issue |
| PostgreSQL CI service-image digests | `.github/workflows/ci.yml` (`postgres:18@sha256:…` in `checks`, `test`, `test-rest`) | `tools/versioncheck` (`.github/workflows/version-coverage.yml`) | Monthly schedule and `workflow_dispatch` | Manual/AI PR updating all three CI job digests from `docker buildx imagetools inspect postgres:18` |
| Downloaded tool binaries (`tailwindcss`, `golangci-lint`) | `tools/download/main.go` (`tailwindVersion`, `lintVersion`, platform SHA-256 hashes) | `tools/versioncheck` (`.github/workflows/version-coverage.yml`) | Monthly schedule and `workflow_dispatch` | Manual/AI PR updating version constants, `darwin-arm64` and `linux-amd64` SHA-256 hashes, `tools/download/main_test.go`, and running `make css` / `make lint` |
| GitHub Actions | `.github/workflows/ci.yml`, `.github/workflows/codeql.yml`, `.github/workflows/sourcemaps-store.yml`, `.github/workflows/version-coverage.yml` | Dependabot (`github-actions`, `/`) | Weekly (7-day cooldown) | Maintainer reviews SHA-pinned action updates |
| Vendored browser assets (`htmx`, `htmx-ext-sse`, `@fluentui/svg-icons`) | `web/static/vendor/README.md`, `web/static/vendor_test.go`, `tools/buildicons/main.go` | Manual-only gap (listed in the monthly tracking issue footer) | Reviewed during monthly coverage triage or when an upstream fix is needed | Manual/AI PR updating asset, SHA-256 in `web/static/vendor_test.go` or `tools/buildicons/main.go`, and versioned asset path |

## Why non-Dependabot pins are not auto-proposed

`.github/dependabot.yml` covers `gomod` (`/`, `/tools`, `/tools/tbls`), `github-actions` (`/`), `npm` (`/`), and `docker-compose` (`/`), each weekly with a seven-day cooldown (`default-days: 7`). The remaining pins cannot be proposed automatically by Dependabot without weakening repository invariants:

- **Go toolchain (`go.mod`, `tools/go.mod`, `tools/tbls/go.mod`):** Dependabot `gomod` updates module requirements rather than proposing a synchronized `go` toolchain bump across all three modules.
- **Node runtime (`.node-version`):** Dependabot `npm` updates `package.json` and `package-lock.json`, not `.node-version` or local/sandbox runtime provisioning.
- **CI PostgreSQL digests (`.github/workflows/ci.yml`):** Dependabot `github-actions` updates `uses:` actions, not job `services:` container images; `docker-compose` scans `compose.yml` (which tracks the major `postgres:18` tag for local development) rather than workflow service digests.
- **Downloaded binaries (`tools/download/main.go`) and vendored assets (`web/static/vendor/`, `tools/buildicons/main.go`):** versions and per-platform or archive SHA-256 checksums live in Go source and tests so `make check` never trusts unverified downloads.

## Monthly version-coverage review

`.github/workflows/version-coverage.yml` builds `tools/versioncheck` without `GITHUB_TOKEN`, then runs the binary with `-sync-issue .` on the first of each month (`17 5 1 * *`) and on `workflow_dispatch`. Manual runs are only possible from `main`; runs dispatched from another branch or a tag skip the job.

- **Observable signal:** `tools/versioncheck/main.go` inspects local pins (including cross-file agreement across the three `go.mod` files and across `compose.yml` and the three `.github/workflows/ci.yml` service digests), queries upstream release/registry endpoints, writes the Markdown table to stdout and `$GITHUB_STEP_SUMMARY`, and creates or updates one open tracking issue titled `build: monthly version-coverage review` (matching only issues authored by `github-actions[bot]` or the repository owner), even when every pin is `up-to-date`.
- **Manual Node review:** Node major upgrades and end-of-life transitions are a manual-only gap; the Node row checks only the pinned major.
- **Failure handling:** failed upstream lookups, local pin drift, or a pin newer than upstream latest are recorded as `check-failed` in the tracking issue and never treated as `up-to-date`; `tools/versioncheck` still syncs the tracking issue before exiting non-zero so both the issue and the workflow run surface the failure. Issue discovery paginates within the configured repository, ignoring pull requests; a listing failure exits with `check-failed` without creating an issue.
- **Credential isolation:** the workflow triggers only on `schedule` and `workflow_dispatch` (never `pull_request`), sets top-level `contents: read`, and grants `issues: write` only to the `review` job gated on `github.repository == 'tkakkie/ribbitto' && github.ref == 'refs/heads/main'`. Checkout explicitly uses `refs/heads/main`.
- **Reproducible validation:** run `go -C tools test -race -count=1 -v ./versioncheck` (also part of `make check` via `check-tools`) to verify the workflow's main-only gate and checkout and exercise the `up-to-date`, `outdated`, and `check-failed` (upstream HTTP failure and local `go.mod` drift) paths with representative Docker token and GitHub release responses, Node indexes listing a newer major first (selecting the newest release in the pinned major, failing when none exists), plus later-page tracking-issue matches and pagination failures, against deterministic local test servers.

## Triage and rollout rules

- **Security fixes:** address actionable findings from `make vuln` (`govulncheck`), CodeQL, Dependabot security alerts, or runtime security releases promptly in a dedicated pull request. If no upstream fix exists yet, report to the maintainer.
- **Ordinary updates:** review open Dependabot pull requests and the `build: monthly version-coverage review` tracking issue at least monthly. Preserve exact pins, SHA-256 checksums, and the seven-day cooldown for routine updates.
- **Major runtime and database upgrades:** handle Go major/minor toolchain transitions, Node major upgrades, and PostgreSQL major upgrades (`postgres:18` to a new major version) as separately approved issues (`ready` from the maintainer). Each issue must cover compatibility checks, cluster data migration (`pg_dump`/`pg_restore`), locale/collation impact ([`database-locales.md`](database-locales.md)), and backup/rollback steps ([`setup-and-backup.md`](setup-and-backup.md)).
- **No automatic merge:** never enable auto-merge or automatic deployment for dependency or runtime updates ([Decision 9](decisions/09-manual-merges.md), [Decision 11](decisions/11-the-maintainer-decides-merges-an-ai-may-execute-them.md)).
