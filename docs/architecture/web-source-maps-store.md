# Source-map store

## Storing job, release naming and retention — #1819

On `main`, `make sourcemaps-store` writes `.js.map` (`sourcesContent`, no
`sourceMappingURL`) outside `web/static/dist`, and `bin/sourcemaps/index.json`
outside `web/static` ([#1819][i1819]).
`.github/workflows/sourcemaps-store.yml`: `sourcemaps` environment,
`GITHUB_TOKEN` `contents: read`, `SOURCEMAPS_TOKEN` only.
`.github/workflows/ci.yml`: `cancel-in-progress: true`, no secret.

Concurrency: `sourcemaps-<full SHA>`, `cancel-in-progress: false`.
Commits may overlap; delete idempotently by exact ID
(404 or absent-confirmed 422 succeeds).
Build once (`make bundles`); upload those bytes (`make sourcemaps-store`).
Secret: upload step-level `env` only, never build/install/test.
SHA-pin Actions; command, Go dependency and Makefile/tool changes require
[review](../workflow/review-operations.md#third-ai-review).

- **Naming:** tag/name:
  `map-<UTC start as YYYYMMDDTHHMMSSZ>-<full SHA>-<run id>.<attempt>`: runner
  job-start clock, read once and passed as the only "now";
  lower-case 40-hex SHA; decimal GitHub run id/attempt without leading zeros.
  Age/order: parsed `(start time, run id, attempt)` with numeric IDs, never `created_at`.
  Never reuse, rename, retag or re-draft releases.
  Target: README-only store `main` (`target_commitish: "main"`);
  create/delete tags with releases.
  Lookup (`go -C tools run ./sourcemaps -lookup <SHA> [<sha256> [<run id> [<attempt>]]]`):
  newest complete published release per SHA and run/attempt filters; attempt
  requires run id; never fall back to another run or attempt.
- **Lifecycle:** create a draft, upload maps and `index.json`
  (`{"commit","run_id","entries":{"<name>":{"file","sha256","map"}}}`), verify
  the manifest, publish, then verify lookup and per-ID fetch-back
  (exact SHA/run/attempt release; identical index and every map's bytes).
  Validate receipts; release and paginated asset views must exactly
  match manifest names, sizes, states, counts and receipt IDs before publication.
  Read up to four pairs 1s apart, then a fresh pair immediately before one re-upload
  round, only for names absent from both views throughout. Read up to four more
  pairs immediately, then 1s apart. Reads have a 15s bound, restarted after re-upload;
  each re-upload has a separate 30s bound. Extras, duplicates, invalid entries and
  any error fail the run. This relies on GitHub rejecting same-name assets (422),
  which fails the run.
  Published releases are complete by construction; only drafts are incomplete.
  Older duplicates take no slot and stay until their SHA expires.
  Never reuse drafts or create prereleases. Idempotently delete abandoned
  strict-`map-` drafts older than 7 days by parsed start time (retried creates
  included), by release ID, unless that SHA's concurrency group holds a run.
  Paginate runs; errors/incomplete/1,000-capped listings fail closed before deletion.
- **Population:** eligible releases are canonical published non-prereleases,
  parsed time at most "now" plus one day. Younger/non-strict drafts (outside
  7-day cleanup), prereleases, unparsable/out-of-range names and name/tag mismatches
  are neither counted nor deleted; warn in job summary. Count each SHA's newest complete published release
  toward the newest 10 and 90-day floor (**distinct SHAs**, not releases).
  A rerun of an old SHA takes a newest-10 slot by start time,
  potentially pushing a deployed SHA past 90 days;
  production markers independently protect individual releases.
- **Retention:** **90 days provisionally**
  plus a **90-day post-retirement investigation margin** (#2049).
  **90 days is a floor, not a deadline:** retention runs only **after a
  successful upload** on `main`; without pushes, old releases stay. Delete
  eligible published releases only if their SHA's newest is **both** older than
  90 days from "now" and outside the newest 10 distinct SHAs, with no active
  marker or uncompleted retirement margin on the individual release.
  Delete by exact release ID, then exact tag ref only after a reference and
  fresh-list re-check confirms no release shares it; never use
  name lookup alone. Retry failed tag deletions next run via **orphan tags**:
  canonical `map-` tags older than 7 days, referenced by no release of any kind
  (published, draft, prerelease, unparsable, out-of-range or mismatched), with
  the same re-check. Log actual deletions (`%q` name, ID, SHA,
  age in days), warnings and summary; escape command introducers (`##[`, `::`)
  and controls; no secrets/headers. Upload, refused/expired auth or
  deletion failures fail visibly; failed uploads skip
  retention and delete nothing.

## Production markers — #2049 (Part 2/2)

Before distribution/startup, successfully verify and mark the receipt's exact release:
`go -C tools run ./sourcemaps -production <release name> active` with store access;
stop on failure. The writer owns status across all instances: retire after the
last stops; rollback requires verification and re-marking `active` before startup, clearing retirement.

Reserve release body for marker: empty/null means ordinary;
exactly `sourcemaps-production-v1 active`: in production;
`sourcemaps-production-v1 retired <UTC RFC3339 timestamp>`: retirement. Reject extra text,
whitespace, offsets, fractional seconds, unknown versions, malformed/future
times. Retire via the same command with `retired` instead of `active`;
retries retain the marker and first retirement time. Use a trusted writer clock:
a clock set far back shortens the margin; one set far forward writes a future
retirement time, making `-production` and cleanup refuse until the maintainer
replaces the release body with a valid marker by hand in the private store repository.
Never retire unmarked releases or overwrite invalid markers. Patch only canonical
published non-prerelease bodies, never assets/name/tag; retry failed writes first.

Before **any** deletion (draft/orphan-tag cleanup included), validate all listed
bodies (GET by exact ID only if absent, not null). Unreadable/invalid/unknown-version
markers abort with **nothing deleted**. Active releases stay regardless of age/SHA
position; retired releases stay at least 90 days after retirement. Then **both**
floors apply: SHA's newest release older than 90 days and outside the newest 10
distinct SHAs. Same-SHA releases have separate markers/margins; absent markers
add no protection.

Immediately before each release DELETE, GET its exact ID; compare body with the
validated snapshot. Read errors or changes (including marks/retirements
since listing) abort that delete and remaining cleanup; prior deletions stand.
A stale listing can leave earlier deletions standing before this re-read stops
the run, so **nothing deleted** is guaranteed only for the listed bodies. GitHub
has no atomic marker read-and-delete. Until [#2145][i2145] holds, no real production
distribution/deployment: marking (rollback re-marking/retirement included) and
cleanup deletions need one mutual exclusion across the final GET/DELETE and
different SHAs/runs, checked with the real API and a race test; before every
distribution, `-verify` (#2131) must pass; distribute its exact verified bundle bytes
unchanged. Cleanup takes no lock; per-SHA upload concurrency groups provide none.
This PR adds no deploy step. Tests: `TestProductionRetention`, `TestProductionTransitions`.

## Exact-build verification — #2049 (Part 1/2)

Before distribution/deployment:
`go -C tools run ./sourcemaps -verify <bundle dir> <SHA> <run id> <attempt>`
under existing store credential rules, on that run/attempt's complete production
bundle directory. Only top-level `.js`, regular-file targets (follow symlinks); ignore
`manifest.json`, `X.JS`, nested bundles and
others. Require a complete published exact SHA/run/attempt release; match bundle
filenames/SHA-256 bytes to index. Missing releases,
mismatched bytes/names, missing/extra bundles, non-regular targets, empty indexes
or read failures exit nonzero. Each indexed map needs a non-empty uploaded asset.
Read-only: no map downloads/content checks or substitute uploads.

Success stdout JSON: `commit`, `run_id`, `attempt`, full store release name `release`,
`entries` (per bundle: `file`, `sha256`, indexed map
asset `map`). Retain it outside public assets with distribution record; stop on
verification failure and distribute exact bytes without rebuilding/changing them.
Use receipt SHA/bundle hash/run id/attempt in `-lookup`;
SHA-only lookup is insufficient for distribution.

The production gate stays closed until both #2049 parts merge.
Verify and mark before distribution/deploy. Required: #1819's first real store AND
fetch record (main run 41571339027 recorded). Real deletion pending on #1819
(first run: zero releases/tags deleted) does not block the first production release;
its preparation states deletion recorded or still pending.

[i1819]: https://github.com/tkakkie/ribbitto/issues/1819
[i2145]: https://github.com/tkakkie/ribbitto/issues/2145
