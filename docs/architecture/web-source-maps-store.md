# Source-map store

Read when: changing the private source-map store's job, release naming or retention.

## Storing job, release naming and retention — #1819

[#1819][i1819] writes production `.js.map` files (`sourcesContent`, outside
`web/static/dist`, no `sourceMappingURL`) and `bin/sourcemaps/index.json`
(outside `web/static`), uploads them, runs retention and the real-credential
check on `main`, and adds `httptest` (`make check-tools`) and leak tests via
`tools/sourcemaps` (`make sourcemaps-store`) and
`.github/workflows/sourcemaps-store.yml` (`sourcemaps` environment,
`GITHUB_TOKEN` `contents: read`, `SOURCEMAPS_TOKEN` only;
`.github/workflows/ci.yml` keeps `cancel-in-progress: true`, no secret).

The job uses a per-commit concurrency group (`sourcemaps-<full SHA>`,
`cancel-in-progress: false`), as a shared group keeps one running and one
pending run and cancels the rest (silently dropping maps, since cancellation is
not a failure); runs for different commits may overlap, so deletions are
idempotent by exact ID (404 or absent-confirmed 422 is success) and no step
assumes it runs alone. It builds once (`make bundles`) and uploads that output
(`make sourcemaps-store`), passing the secret only to the upload step
(step-level `env`), never build/install/test; actions are SHA-pinned; and the
command, Go dependencies and Makefile targets/tools are reviewed as workflow
changes ([third-AI review](../workflow/review-operations.md#third-ai-review)).

- **Release naming, order and uniqueness:** tag and name
  `map-<UTC start as YYYYMMDDTHHMMSSZ>-<full SHA>-<run id>.<attempt>` (runner
  clock is the only "now", read once at job start and passed to the command;
  lower-case 40-hex SHA; decimal GitHub run id/attempt without leading zeros,
  preventing same-SHA same-second collisions); maps and index are its assets.
  Age and order use the parsed name (never GitHub's `created_at`), by `(start
  time, run id, attempt)` (`run id`/`attempt` numeric). Releases are never
  reused, renamed, retagged or returned to draft; they target store `main`
  (`target_commitish: "main"`, README-only store) and tags are created/deleted
  with their release. Reruns or second pushes make new releases; #2049 and
  lookup (`go -C tools run ./sourcemaps -lookup <SHA> [<sha256> [<run id> [<attempt>]]]`)
  resolve a SHA to its newest complete published release within the supplied
  run/attempt filters. An attempt requires a run id; exact selection never
  falls back to another run or attempt.
- **Release lifecycle:** each run creates a draft, uploads maps and `index.json`
  (`{"commit","run_id","entries":{"<name>":{"file","sha256","map"}}}`), verifies
  them against that manifest, publishes, and verifies lookup and per-ID
  fetch-back; published releases are complete by construction; only drafts are
  incomplete. A failed or interrupted run leaves its draft or (after publish) a
  published duplicate, both harmless under distinct-SHA retention. Drafts are
  never reused; a later run idempotently deletes abandoned strict-`map-` drafts
  older than 7 days by parsed start time (including retried creates) by release
  ID unless that SHA's concurrency group holds a run (paginating runs and
  failing closed before any deletion on check errors or incomplete/1,000-capped
  listings). The job never creates prereleases.
- **Population and bounds:** only published, non-prerelease releases whose tag
  equals the name and full-matches the canonical `map-` pattern above with
  parsed time at most "now" plus one day are eligible; younger or non-strict
  drafts (outside the 7-day cleanup), prereleases, unparsable (invalid time,
  SHA, run id or attempt), out-of-range (future-dated: invisible, never deleted)
  and name/tag-mismatch releases are neither counted nor deleted and warn in the
  job summary. Retention counts **distinct SHAs**, not releases: only each SHA's
  newest complete published release (by the order above) counts toward the
  newest 10 and the 90-day floor; older published duplicates take no slot and
  stay until that SHA expires. A rerun of an old SHA takes a newest-10 slot by
  start time and can push a deployed SHA past 90 days; slot order is by
  publication run; production markers protect individual releases independently.
- **Retention, deletion and failure handling:** **90 days provisionally**
  (2026-11-01), with a **90-day post-retirement investigation margin** (#2049).
  **90 days is a floor, not a deadline:** retention runs
  opportunistically only **after a successful upload** on `main` (without new
  `main` pushes, old releases stay: push-time, not automatic expiry). It deletes
  a SHA's eligible published releases only when that SHA's newest release is
  **both** older than 90 days (from "now") and outside the newest 10 distinct
  SHAs, and that individual release has no active marker and has completed any
  post-retirement margin — each release first by exact ID, and only after that
  succeeds its tag by
  exact ref once the reference and fresh-list re-check confirms no release
  shares it (never by name lookup alone). Failed tag deletions wait for the next
  run, which collects and deletes **orphan tags** — canonical `map-` tags older
  than 7 days referenced by no release of any kind (published, draft,
  prerelease, unparsable, out-of-range or mismatched) and confirmed orphaned by
  that re-check. Actual release/tag deletions (`%q` name, ID, SHA, age in days),
  warnings and a summary line escape command introducers (`##[`, `::`) and
  control chars, without secrets or headers. Failed uploads, refused/expired
  auth or failed deletions fail visibly; upload failures skip retention and
  delete nothing.

## Production markers — #2049 (Part 2/2)

The future distribution/deploy step marks the exact store release in the
verification receipt using
`go -C tools run ./sourcemaps -production <release name> active` with store
access. Verify and mark successfully **before** distribution or startup; stop
on failure. No distribution/deploy step is introduced here. The writer owns
status across all instances: retire only after the last stops; for rollback,
verify and re-mark the target `active` before startup (clearing retirement).

The release body is reserved for the marker: empty or null means an ordinary
release; exactly `sourcemaps-production-v1 active` means still in production;
`sourcemaps-production-v1 retired <UTC RFC3339 timestamp>` records retirement
(e.g. `sourcemaps-production-v1 retired 2026-11-03T12:00:00Z`). No extra text,
whitespace, offsets or fractional seconds are accepted. Unknown versions,
malformed or future retirement times are errors. To retire, use the same command
with `retired` instead of `active`; it retains the marker and first retirement
time on retry. Retirement uses the writer’s clock: a clock set far back shortens
the margin, so the writer must use a trusted time source. An unmarked release
cannot be retired; invalid markers are never overwritten by the tool. Marker
writes patch only the body of a canonical published non-prerelease release,
never its assets, name or tag. A failed write must be retried before proceeding.

Before any deletion, including draft/orphan-tag cleanup, retention validates
every listed body (GET by exact ID if absent or null). Any unreadable, invalid
or unknown-version marker aborts with **nothing deleted**. Active releases stay
regardless of age or SHA position. Retired releases stay for at least 90 days
after retirement; deletion then requires **both** existing floors (the SHA's
newest release older than 90 days and outside the newest 10 distinct SHAs).
Same-SHA releases keep separate markers and margins. An absent marker adds no
protection. The provisional floor and margin are minimums; deletion remains
opportunistic after successful uploads.

Immediately before each release DELETE, cleanup re-reads that exact release and
compares its body with the validated snapshot. Any read error or marker change
(including a concurrent mark or retirement) aborts that delete and the remaining
cleanup; prior deletions cannot be undone. Thus a marker written between listing
and this final read cancels a pending delete. GitHub provides no atomic marker
read-and-delete here. Serializing marking with cleanup and status writers with
each other is a prerequisite of the first distribution/deploy issue, covering
the final GET/DELETE interval. Cleanup takes no lock; per-SHA upload concurrency
groups provide none. `TestProductionRetention` and `TestProductionTransitions`
use only a fake store.

## Exact-build verification — #2049 (Part 1/2)

Before any future distribution or deployment step, run
`go -C tools run ./sourcemaps -verify <bundle dir> <SHA> <run id> <attempt>`
with store access (the existing credential rules still apply). Use the actual
production bundle directory, containing the complete set of top-level `.js`
bundles from that producing run/attempt, such as `../web/static/dist` (relative
to `tools/` because of `-C tools`). Only top-level names ending exactly in `.js`
are checked; their targets must be regular files (symlinks are followed).
Other names, including `manifest.json`, `X.JS` and nested bundles, are ignored.
The command requires a complete published release for that exact SHA, run id
and attempt, hashes every bundle's bytes with SHA-256, and checks both its file
name and hash against the stored index. Missing releases, mismatched bytes or
names, missing/extra bundles, non-regular targets, empty indexes and read failures
exit nonzero. Each indexed map must have a non-empty uploaded release asset;
verification does not download maps or check their contents. It only reads the
store and never uploads a substitute for a missing release.

On success, stdout is a JSON receipt with `commit`, `run_id`, `attempt`,
`release` (the full store release name), and `entries` (each bundle's `file`,
`sha256` and indexed map asset name `map`). The future distribution/deploy step must
retain this receipt outside public assets with the build's distribution record,
stop on verification failure, and distribute those exact verified bytes without rebuilding or
changing them. For investigation, use the receipt's SHA, bundle hash, run id
and attempt with `-lookup`; this selects the corresponding map even when a
later same-SHA rebuild differs. SHA-only lookup is insufficient evidence for
distribution. `TestExactBuildVerification` exercises this against a fake store.

This introduces no distribution step. The #2049 production gate closes when both
parts have merged; future distribution and deployment implementations must wire
verification and production marking before distribution. #1819's real deletion
record is still pending (the first run deleted zero releases/tags); no production
release is made while that record is missing.

[i1819]: https://github.com/tkakkie/ribbitto/issues/1819
