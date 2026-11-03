# Source-map store

## Storing job, release naming and retention — #1819

[#1819][i1819]: on `main`, `tools/sourcemaps` (`make sourcemaps-store`) uploads
production `.js.map` (`sourcesContent`, outside `web/static/dist`, no
`sourceMappingURL`) and `bin/sourcemaps/index.json` (outside `web/static`).
Tests: `make check-tools`, including leak tests. `.github/workflows/sourcemaps-store.yml`: `sourcemaps` environment,
`GITHUB_TOKEN` `contents: read`, `SOURCEMAPS_TOKEN` only.
`.github/workflows/ci.yml`: `cancel-in-progress: true`, no secret.

Use per-commit concurrency (`sourcemaps-<full SHA>`, `cancel-in-progress: false`):
a shared group cancels all but one running and one pending run, silently dropping
maps. Different commits may overlap; deletions must be idempotent by exact ID
(404 or absent-confirmed 422 succeeds), with no assumption of running alone.
Build once (`make bundles`); upload those bytes (`make sourcemaps-store`).
Secret: upload step-level `env` only, never build/install/test.
Actions are SHA-pinned; command, Go dependencies and Makefile targets/tools need
workflow review ([third-AI review](../workflow/review-operations.md#third-ai-review)).

- **Release naming:** tag and name are
  `map-<UTC start as YYYYMMDDTHHMMSSZ>-<full SHA>-<run id>.<attempt>`: runner
  clock read once at job start is the only "now", passed to the command;
  lower-case 40-hex SHA; decimal GitHub run id/attempt without leading zeros
  avoid same-SHA same-second collisions. Age/order
  use the parsed name, never `created_at`: `(start time, run id, attempt)`,
  with numeric run id/attempt. Never reuse, rename, retag or return releases
  to draft. They target README-only store `main` (`target_commitish: "main"`);
  create/delete tags with releases.
  #2049 lookup (`go -C tools run ./sourcemaps -lookup <SHA> [<sha256> [<run id> [<attempt>]]]`):
  newest complete published release per SHA within run/attempt filters; attempt
  requires run id, with no fallback.
- **Release lifecycle:** create a draft, upload maps and `index.json`
  (`{"commit","run_id","entries":{"<name>":{"file","sha256","map"}}}`), verify
  against that manifest, publish. Only drafts are incomplete.
  Never reuse drafts or create prereleases. Idempotently delete abandoned
  strict-`map-` drafts older than 7 days by parsed start time (retried creates
  included), by release ID, unless that SHA's concurrency group holds a run.
  Paginate runs; errors/incomplete/1,000-capped listings fail closed before deletion.
- **Population and bounds:** eligible releases are published non-prereleases
  whose tag equals their canonical `map-` name, parsed time at most "now" plus
  one day. Younger/non-strict drafts (outside 7-day cleanup), prereleases,
  unparsable names (invalid time, SHA, run id or attempt), future-dated
  out-of-range names and name/tag mismatches are neither counted nor deleted;
  warn in the job summary. Count **distinct SHAs**, not releases: each SHA's
  newest complete published release by the above order counts toward the
  newest 10 and 90-day floor. Older duplicates take no slot and stay until
  that SHA expires. A rerun of an old SHA takes a newest-10 slot by start time
  (publication run order), potentially pushing a deployed SHA past 90 days;
  production markers independently protect individual releases.
- **Retention, deletion and failure handling:** **90 days provisionally**
  plus a **90-day post-retirement investigation margin** (#2049).
  **90 days is a floor, not a deadline:** retention runs only **after a
  successful upload** on `main`; without pushes, old releases stay. Delete
  eligible published releases only if their SHA's newest release is **both**
  older than 90 days from "now" and outside the newest 10 distinct SHAs, and
  the individual release has no active marker and has completed any retirement
  margin. Delete by exact release ID, then its exact tag ref only after a
  reference and fresh-list re-check confirms no release shares it; never use
  name lookup alone. Retry failed tag deletions next run via **orphan tags**:
  canonical `map-` tags older than 7 days, referenced by no release of any kind
  (published, draft, prerelease, unparsable, out-of-range or mismatched), with
  that re-check confirming orphanhood. Log actual deletions (`%q` name, ID, SHA,
  age in days), warnings and summary; escape command introducers (`##[`, `::`)
  and control chars, with no secrets/headers. Upload, refused/expired auth or
  deletion failures fail visibly; failed uploads skip
  retention and delete nothing.

## Production markers — #2049 (Part 2/2)

Future distribution/deploy marks the receipt's exact store release with
`go -C tools run ./sourcemaps -production <release name> active` and store access. Verify and mark successfully **before** distribution/startup;
stop on failure. The writer owns status across all instances: retire after the
last stops; rollback requires verification and re-marking `active` before startup, clearing retirement.

Reserve the release body for the marker: empty/null means ordinary release;
exactly `sourcemaps-production-v1 active` means in production;
`sourcemaps-production-v1 retired <UTC RFC3339 timestamp>` records retirement
(e.g. `sourcemaps-production-v1 retired 2026-11-03T12:00:00Z`). Reject extra text,
whitespace, offsets, fractional seconds, unknown versions, malformed/future
times. Retire with the same command using `retired` instead of `active`;
retries retain the marker and first retirement time. Use a trusted writer clock:
a clock set far back shortens the margin; one set far forward writes a future
retirement time, making `-production` and cleanup refuse until the maintainer
replaces the release body with a valid marker by hand in the private store repository.
Unmarked releases cannot be retired;
never overwrite invalid markers. Patch only the body of a canonical published
non-prerelease, never assets/name/tag. Retry failed writes before proceeding.

Before **any** deletion, including draft/orphan-tag cleanup, validate every
listed body (GET by exact ID only if absent, not null). Unreadable, invalid or
unknown-version markers abort with **nothing deleted**. Active releases stay
regardless of age/SHA position; retired releases stay at least 90 days after
retirement. Then **both** floors still apply: the SHA's newest release older
than 90 days and outside the newest 10 distinct SHAs. Same-SHA releases have
separate markers/margins; absent markers add no protection.

Immediately before each release DELETE, re-read its exact ID and compare body
with the validated snapshot. Read errors or changes (including marks/retirements
since listing) abort that delete and remaining cleanup; prior deletions stand.
A stale listing can leave earlier deletions standing before this re-read stops
the run, so **nothing deleted** is guaranteed only for the listed bodies. GitHub
has no atomic marker read-and-delete. The first distribution/deploy issue must
serialize marking with cleanup and status writers with each other, covering the
final GET/DELETE interval. Cleanup takes no lock; per-SHA upload concurrency
groups provide none. Tests: `TestProductionRetention`, `TestProductionTransitions`.

## Exact-build verification — #2049 (Part 1/2)

Before future distribution/deployment, run
`go -C tools run ./sourcemaps -verify <bundle dir> <SHA> <run id> <attempt>`
with store access under existing credential rules. Supply that run/attempt's
complete production bundle directory, e.g. `../web/static/dist` (relative to
`tools/` with `-C tools`). Check only top-level `.js` names with regular-file
targets (follow symlinks); ignore `manifest.json`, `X.JS`, nested bundles and
other names. Require a complete published release for the exact SHA/run/attempt
and match bundle filenames and SHA-256 bytes to its index. Missing releases,
mismatched bytes/names, missing/extra bundles, non-regular targets, empty indexes
or read failures exit nonzero. Each indexed map needs a non-empty uploaded asset.
Verification is read-only: no map downloads/content checks or substitute uploads.

Success stdout is a JSON receipt: `commit`, `run_id`, `attempt`, `release`
(full store release name), `entries` (per bundle: `file`, `sha256`, indexed map
asset `map`). Future distribution/deploy must retain it outside public assets
with the build's distribution record, stop on verification failure, and distribute
those exact bytes without rebuilding/changing them. Use receipt SHA, bundle hash,
run id and attempt in `-lookup` for the right map across differing same-SHA
rebuilds; SHA-only lookup is insufficient for distribution.
Test: `TestExactBuildVerification`.

Both #2049 parts must merge to close the
production gate; future distribution/deploy must verify and mark first. #1819's
real deletion record remains pending (first run: zero releases/tags deleted);
no production release while missing.

[i1819]: https://github.com/tkakkie/ribbitto/issues/1819
