# Web source maps

Source-map storage and delivery for the [web toolchain](web-toolchain.md).

## Source maps — #1411

In development (`webbuild -dev` / `-watch`, #1821), bundles, external
`<name>-<hash>.js.map` files (`sourcesContent` and
`//# sourceMappingURL=<name>-<hash>.js.map`) and `manifest.json` (`"map"` fields
and retained recent generations) go to `bin/dev-dist`. With
`RIBBITTO_DEV_ASSETS=bin/dev-dist`, the static handler serves registered bundles
and `.js.map` files from disk (`Cache-Control: no-store`).

Production builds (`make bundles`) write `web/static/dist` without maps,
`sourceMappingURL` comments or `"map"` fields; dev builds stay in `bin/dev-dist`
out of the embedded tree, whose handler never serves `.map` files. Production
maps (#1819) follow
[decision 43](../decisions/43-typescript-with-a-go-driven-build.md) and the
store below (#1469): a private map index beside the maps outside `web/static`
(never in `manifest.json`) ties each map to its bundle by build SHA, run id and
bundle content hash.

Production keeps maps out of the image, embedded files and public static routes
(no inline maps or public `sourceMappingURL`), matches stacks to exact output,
and verifies CSP and delivery in [target browsers](browser-targets.md) with
version-specific evidence under its verification rules (classic-bundle browser
check; verify module/shared/lazy/preload before adoption without loosening the
nonce-only CSP).

## Private production store — #1469, #1819

Under [decision 43](../decisions/43-typescript-with-a-go-driven-build.md)
(2026-10-21; method 2026-10-25 in #1469), production maps and their index are
**private GitHub Release assets** in the maintainer-only repo
`tkakkie/ribbitto-sourcemaps`: inside GitHub, no external service, and **no map
is ever committed to Git** (Actions artifacts do not fit public-repo CI).

- **Store, access and setup:** `tkakkie/ribbitto-sourcemaps` is private (README
  only, no collaborators, no Pages, Issues/Wiki/Projects disabled). Only the
  maintainer reads or administers it; only CI writes. No AI holds the token or
  reads the store (AIs read only `tkakkie/ribbitto` runs via `gh` with their own
  token). Setup: (1) create that repo; (2) create a maintainer 90-day
  fine-grained token for that repo only with **Contents: Read and write**
  (`Metadata: Read-only` automatic); (3) in `tkakkie/ribbitto` (`Settings →
  Environments`), create `sourcemaps` (`main` only) and secret
  `SOURCEMAPS_TOKEN` (never log or expose a token).
- **Evidence:** `gh api repos/tkakkie/ribbitto/environments/sourcemaps` and
  `.../deployment-branch-policies` confirm `sourcemaps` (`2026-10-25T21:15:14Z`,
  custom branch policy `main`). AI tokens get 403 on environment secrets and
  cannot see the private store or token scope, so evidence combines that check
  with #1469 comments [6403307193][c1] and [6403484872][c2] (store created
  `2026-10-26`; `SOURCEMAPS_TOKEN` created `2026-10-25 21:21 UTC`,
  `tkakkie/ribbitto-sourcemaps` only, `Contents: Read and write`, expiring about
  `2027-01-23`). Naming the private repo, token scope, permission and expiry is
  intentional (no value, token ID or private URL). #1819's first `main` run
  proves the token works.

### Storing job, release naming and retention — #1819

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
  fetch-back; published releases are complete by construction and only drafts
  are incomplete. A failed or interrupted run leaves its draft or (if it failed
  after publishing) a published duplicate, both harmless under distinct-SHA
  retention. Drafts are never reused; a later run idempotently deletes abandoned
  strict-`map-` drafts older than 7 days by parsed start time (including
  retried-create duplicates) by release ID unless that SHA's concurrency group
  holds a run (paginating runs and failing closed before any deletion on check
  errors). The job never creates prereleases.
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
  `main` pushes, old releases stay: push-time deletion, not automatic expiry).
  It deletes a SHA's eligible published releases only when that SHA's newest
  release is **both** older than 90 days (from "now") and outside the newest 10
  distinct SHAs by the order above — each release first by exact ID, and only
  after that succeeds its tag by exact ref once a shared reference and
  fresh-list re-check confirms no remaining release shares it (never by name
  lookup alone). Failed tag deletions wait for the next run, which collects and deletes **orphan tags** — canonical `map-` tags older than 7 days
  (by parsed start time) referenced by no release of any kind (published, draft,
  prerelease, unparsable, out-of-range or mismatched) and confirmed orphaned by
  that re-check. Actual release/tag deletions (`%q` name, ID, SHA, age in days)
  and a summary line are logged without secrets or headers. Failed uploads,
  refused/expired auth or failed deletions fail visibly; upload failures skip
  retention and delete nothing.

### Constraints: production gate, rotation and residual risk

- **Gate before the first production release ([#2049][i2049]):** "the newest 10"
  protects almost nothing at AI merge pace, only the 90 days do. #2049 ensures a
  release exists for the SHA of every distributed or deployed build and decides
  how production releases' maps are kept (marker, revised retention rule and
  post-retirement margin here, and tests in #1819's command). No container
  image, Dockerfile, release workflow, tagged or distributed binary (including
  `go install`), manual deployment or other build distribution outside CI is
  allowed before #2049.
- **Token rotation ([#2050][i2050]):** `SOURCEMAPS_TOKEN` expires about
  `2027-01-23`; the maintainer rotates it. Reminders are best effort (desktop
  reminders for `2027-01-09` and `2027-01-20` set `2026-11-01` running if the
  Claude app is open or on next launch, assigned issue #2050 due `2027-01-09` to
  rotate the secret, update the expiry here and open the next issue, and #1819's
  job expiry check), not a guarantee. That check runs **after** upload and
  retention (so a near-expiry token still stores the build's maps), reads
  `github-authentication-token-expiration` (`YYYY-MM-DD HH:MM:SS UTC`), and
  fails visibly under 14 days remaining (missing/unparsable headers warn). The
  failed Actions run is the only assured signal (notification depends on the
  pusher's settings), hence the reminders and #2050.
- **Residual risk:** a job merged to `main` gets the secret automatically;
  `sourcemaps` enforces only branch `main` (no reviewer or wait timer); and any
  AI token can merge
  ([workflow rules](../workflow/README.md#what-github-enforces-and-what-is-only-a-rule)),
  so independent review and maintainer merge approval are rules, not enforced
  controls. Blast radius: read, delete or replace stored maps and write to the
  store repo (a leaked token could be echoed encoded in logs). A required
  environment reviewer would add a human gate per upload, against the
  no-manual-step decision (still the maintainer's option).

[i1819]: https://github.com/tkakkie/ribbitto/issues/1819
[i2049]: https://github.com/tkakkie/ribbitto/issues/2049
[i2050]: https://github.com/tkakkie/ribbitto/issues/2050
[c1]: https://github.com/tkakkie/ribbitto/issues/1469#issuecomment-6403307193
[c2]: https://github.com/tkakkie/ribbitto/issues/1469#issuecomment-6403484872
