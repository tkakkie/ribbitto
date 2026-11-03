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
  lookup (`go -C tools run ./sourcemaps -lookup <SHA> [<sha256> [<run id>]]`)
  resolve a SHA to its newest complete published release.
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
  publication run, and deployed-SHA protection is the #2049 gate's job.
- **Retention, deletion and failure handling:** **90 days provisionally**
  (2026-11-01), revisited at #2049 so a map outlives its release plus an
  investigation margin. **90 days is a floor, not a deadline:** retention runs
  opportunistically only **after a successful upload** on `main` (without new
  `main` pushes, old releases stay: push-time, not automatic expiry). It deletes
  a SHA's eligible published releases only when that SHA's newest release is
  **both** older than 90 days (from "now") and outside the newest 10 distinct
  SHAs — each release first by exact ID, and only after that succeeds its tag by
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

[i1819]: https://github.com/tkakkie/ribbitto/issues/1819
