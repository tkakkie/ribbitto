# Web source maps

Source-map storage and delivery rules for the [web toolchain](web-toolchain.md).

## Source maps — #1411

In development (`webbuild -dev` and `webbuild -watch`, #1821), bundles, external
`<name>-<hash>.js.map` files with `sourcesContent` and a
`//# sourceMappingURL=<name>-<hash>.js.map` comment, and `manifest.json` (with
each entry's `"map"` field and retained recent generations) are written to
`bin/dev-dist`, separate from `web/static/dist`. With
`RIBBITTO_DEV_ASSETS=bin/dev-dist`, the static handler serves registered bundles
and `.js.map` files from disk with `Cache-Control: no-store`.

Production builds (`make bundles`) write `web/static/dist` and omit source maps,
`sourceMappingURL` comments and `"map"` manifest fields; because development
builds write only to `bin/dev-dist`, a development build cannot leak into the
embedded production tree. The embedded production handler never serves `.map`
files. Production maps ([#1819][i1819]) follow
[decision 43](../decisions/43-typescript-with-a-go-driven-build.md)'s approved
private-storage policy and the store contract below ([#1469][i1469]): a separate
private map index written beside the maps outside `web/static` (never in the
runtime `manifest.json` under `web/static/dist`) ties each map file to its
bundle by build SHA, run id and bundle content hash.

In production, keep maps out of the image, embedded files and public static
routes; no inline maps or public `sourceMappingURL` link. Match a stack to the
exact output.
Verify CSP and delivery in the [target browsers](browser-targets.md), with
version-specific evidence under that document's verification rules. The
classic bundle needs a browser check; module/shared/lazy/preload delivery
needs verification before adoption, without loosening the nonce-only CSP.

## Private production store — #1469, #1819

Under [decision 43](../decisions/43-typescript-with-a-go-driven-build.md)
(confirmed 2026-10-21; method decided 2026-10-25 in [#1469][i1469]), production
source maps and their separate map index are stored as **private GitHub Release
assets** in the maintainer-only repository `tkakkie/ribbitto-sourcemaps`.
Storage stays inside GitHub with no new external service, and **no map is ever
committed to a Git repository** (private Actions artifacts do not fit a public
repository's CI).

- **Store, access and setup:** `tkakkie/ribbitto-sourcemaps` is private (README
  only, no collaborators, no Pages, Issues/Wiki/Projects disabled). Only the
  maintainer reads or administers it; only CI writes to it. No AI holds the
  token or reads the store; an AI reads only workflow results on
  `tkakkie/ribbitto` via `gh` with its own token. To reproduce: (1) create that
  private repository; (2) under the maintainer's account, create a fine-grained
  token scoped to `tkakkie/ribbitto-sourcemaps` only with **Contents: Read and
  write** (`Metadata: Read-only` automatic) and a 90-day expiry; (3) in
  `tkakkie/ribbitto` (`Settings → Environments`), create `sourcemaps`, restrict
  deployment branches to `main`, and store the token as secret
  `SOURCEMAPS_TOKEN`. Never log or expose a token value.
- **Recorded evidence:** `gh api repos/tkakkie/ribbitto/environments/sourcemaps`
  and `.../deployment-branch-policies` confirm `sourcemaps`
  (`2026-10-25T21:15:14Z`, custom branch policy `main`). Because AI tokens lack
  `Environments: read` for secrets (403) and cannot inspect the private store or
  token scope, accepted evidence combines that check with [comment
  6403307193][c1] and [comment 6403484872][c2] on [#1469][i1469] (store created
  `2026-10-26`; `SOURCEMAPS_TOKEN` created `2026-10-25 21:21 UTC`,
  `tkakkie/ribbitto-sourcemaps` only, `Contents: Read and write`, expiring about
  `2027-01-23`). Disclosing the private repository name, the token's scope,
  permission and expiry date is a conscious choice (no value, no token ID, no
  private URL). The first automatic run on `main` in [#1819][i1819] proves the
  token works.

### Storing job, release naming and retention — #1819

[#1819][i1819] generates production `.js.map` files (with `sourcesContent`,
outside `web/static/dist`, no `sourceMappingURL` comment) and the separate map
index (outside `web/static`), uploads them, runs retention and the
real-credential check automatically on `main` (the maintainer runs nothing by
hand), and adds mock-API (`httptest` in `make check-tools`) and public-artifact
leak tests. It adds a Go storing command in the tools module and a non-PR
workflow file named sourcemaps-store.yml under the GitHub workflows directory
(named in plain words until [#1819][i1819] creates it) on pushes to `main` only,
using the `sourcemaps` environment and minimal `GITHUB_TOKEN` permissions
(`contents: read`; the store is reached only via `SOURCEMAPS_TOKEN`;
`.github/workflows/ci.yml` keeps `cancel-in-progress: true` and gains no secret
or credentialed job).

The job uses a concurrency group per commit (`sourcemaps-` plus the full SHA)
with `cancel-in-progress: false`, because a group shared by all pushes keeps one
running and one pending run and cancels the others (a cancelled run is not a
failure, so a commit's maps would be dropped silently); runs for different
commits may overlap, so every deletion is idempotent by exact ID (a 404 counts
as done) and no step assumes it runs alone; and a release must exist for the SHA
of every distributed or deployed build (gate [#2049][i2049]). Because the
credentialed job also runs build tooling, the secret is exposed to the upload
step only (step-level `env`), never to build, install or test steps; actions are
pinned by commit SHA; and the storing command and its Go dependencies are
reviewed as if they were workflow changes
([third-AI review](../workflow/delivery.md#third-ai-review)).

- **Release naming, order and uniqueness:** one release per production build,
  tag and name `map-<UTC build time as YYYYMMDDTHHMMSSZ>-<full build SHA>`
  (workflow run start time, read once and passed to the command); maps and their
  index are its assets. Age and order are parsed from the release name, never
  GitHub's `created_at` (which follows the commit), in total string order:
  parsed time, then full SHA. Before creating a release, the command looks for
  any release whose name ends with the same full SHA and, on a match (a rerun,
  even with a new run start time), reuses it and replaces its assets, so a SHA
  never has two releases.
- **Population and bounds:** only published, non-prerelease releases whose name
  matches a strict full-match `map-` pattern and whose parsed time is not later
  than the run start plus one day are counted for the newest 10 and considered
  for deletion; drafts, prereleases, unparsable, future-dated and
  name/tag-mismatch releases are never counted nor deleted and are reported as a
  warning in the job summary; "now" is the run start time passed to the command.
- **Retention, deletion and failure handling:** **90 days provisionally**
  (2026-11-01), revisited at [#2049][i2049] so a map outlives its release plus
  an investigation margin. **90 days is a floor, not a deadline:** retention
  runs opportunistically only **after a successful upload** on a push to `main`
  (if `main` is not pushed after 90 days, old releases stay: push-time deletion
  with the newest 10 protected, not automatic expiry). It deletes counted
  releases **both** older than 90 days (from "now") and outside the newest 10 by
  the total order above — each release first by exact release ID (idempotent,
  404 counts as done) and then its tag by exact tag ref after verifying the name
  parses, equals the tag, and matches the strict full-match `map-` pattern
  (never by name lookup alone). A tag whose deletion failed is left for the next
  run, which deletes only **orphan tags** — strict `map-` tags referenced by no
  release of any kind (published, draft, prerelease, or kept as unparsable,
  future-dated or name/tag-mismatched). A failed upload, refused/expired
  authentication, or failed release or tag deletion fails the job visibly; an
  upload failure skips retention and deletes nothing.

### Constraints: production gate, rotation and residual risk

- **Gate before the first production release ([#2049][i2049]):** "the newest 10"
  protects almost nothing at the pace of AI merges, only the 90 days do.
  [#2049][i2049] ensures a release exists for the SHA of every distributed or
  deployed build and decides how maps of releases still in production are kept
  (marker, revised retention rule and post-retirement margin here, and tests in
  [#1819][i1819]'s command). No container image, Dockerfile, release workflow,
  tagged or distributed binary (including `go install`), manual deployment or
  other distribution of a ribbitto build to anyone but CI happens before
  [#2049][i2049] is done.
- **Token rotation ([#2050][i2050]):** `SOURCEMAPS_TOKEN` expires about
  `2027-01-23`; the maintainer rotates it. Reminders are best effort (two
  scheduled desktop reminders for `2027-01-09` and `2027-01-20` set
  `2026-11-01` that run while the Claude app is open or on its next launch, the
  assigned issue [#2050][i2050] due `2027-01-09` which updates the secret,
  records the next expiry here and opens the next issue, and the expiry check in
  [#1819][i1819]'s upload job), not an absolute guarantee. That check runs
  **after** upload and retention (so a near-expiry token still stores the
  current build's maps), reads `github-authentication-token-expiration`
  (`YYYY-MM-DD HH:MM:SS UTC`), and fails visibly when fewer than 14 days remain
  (missing or unparsable headers log a warning). A failed run is visible in the
  Actions UI and notifies the pusher, which may be an AI account.
- **Residual risk:** a credentialed job merged into `main` receives the secret
  automatically; the `sourcemaps` environment has only the `main` branch rule
  (no required reviewers, no wait timer); and any AI token can merge
  ([workflow rules](../workflow/README.md#what-github-enforces-and-what-is-only-a-rule)),
  so independent review and the maintainer's merge decision are rules, not
  enforced controls. The blast radius is: read, delete or replace every stored
  map and write to the store repository (a leaked token could be echoed into
  public logs in an encoded form). A required reviewer on the environment would
  enforce a human gate but puts a manual approval on every upload, against the
  no-manual-step decision, and is left as the maintainer's option.

[i1469]: https://github.com/tkakkie/ribbitto/issues/1469
[i1819]: https://github.com/tkakkie/ribbitto/issues/1819
[i2049]: https://github.com/tkakkie/ribbitto/issues/2049
[i2050]: https://github.com/tkakkie/ribbitto/issues/2050
[c1]: https://github.com/tkakkie/ribbitto/issues/1469#issuecomment-6403307193
[c2]: https://github.com/tkakkie/ribbitto/issues/1469#issuecomment-6403484872

