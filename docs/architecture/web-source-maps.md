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
(private storage confirmed 2026-10-21; method decided 2026-10-25 in
[#1469][i1469]), production source maps and their separate map index are stored
as **private GitHub Release assets** in the maintainer-only repository
`tkakkie/ribbitto-sourcemaps`. Storage stays inside GitHub with no new external
service, and **no map is ever committed to a Git repository**. Private Actions
artifacts do not fit a public repository's CI.

- **Store and access:** `tkakkie/ribbitto-sourcemaps` is private with no
  collaborators, no code, no Pages, and Issues/Wiki/Projects disabled. Only the
  maintainer reads or administers it; only the CI credential writes to it. No AI
  holds the token or reads the store; an AI reads only workflow results on
  `tkakkie/ribbitto` via `gh` with its own token.
- **Credential and environment:** a fine-grained token created by the
  maintainer, scoped to `tkakkie/ribbitto-sourcemaps` only, with **Contents:
  Read and write** (`Metadata: Read-only` added automatically). Stored as secret
  `SOURCEMAPS_TOKEN` of the `sourcemaps` environment in `tkakkie/ribbitto`, with
  deployment branches restricted to `main`. Never log or expose a token value.
- **Reproducing the setup:** (1) create private repository
  `tkakkie/ribbitto-sourcemaps` (README only, no collaborators,
  Issues/Wiki/Projects disabled); (2) under the maintainer's account, create a
  fine-grained token for `tkakkie/ribbitto-sourcemaps` only with `Contents: Read
  and write` and a 90-day expiry; (3) in `tkakkie/ribbitto` (`Settings →
  Environments`), create `sourcemaps`, restrict deployment branches to `main`,
  and store `SOURCEMAPS_TOKEN`.
- **Recorded evidence:** `gh api repos/tkakkie/ribbitto/environments/sourcemaps`
  and `.../deployment-branch-policies` confirm the `sourcemaps` environment
  (`2026-10-25T21:15:14Z`, custom branch policy `main`). Because AI tokens cover
  only `tkakkie/ribbitto` without `Environments: read` for secrets (403 on secret
  listing) and cannot inspect the private store or secret scope, accepted
  evidence combines that check with the earlier recorded check and maintainer
  statements on [#1469][i1469] ([comment 6403307193][c1], [comment
  6403484872][c2]: store created 2026-10-26; `SOURCEMAPS_TOKEN` created
  2026-10-25 21:21 UTC, `tkakkie/ribbitto-sourcemaps` only, `Contents: Read and
  write`, expiring about `2027-01-23`). That the token works is shown by the
  first automatic run on `main` in [#1819][i1819].

### Storing job, release naming and retention — #1819

Generating production `.js.map` files (with `sourcesContent`, outside
`web/static/dist`, no `sourceMappingURL` comment) and the separate map index
(outside `web/static`), uploading them, running retention and the
real-credential check on `main`, and running the mock-API (`httptest` in `make
check-tools`) and public-artifact leak tests belong to [#1819][i1819].

[#1819][i1819] adds a non-PR workflow file named sourcemaps-store.yml under the
GitHub workflows directory (named in plain words until [#1819][i1819] creates
it) and a Go storing command in the tools module. The workflow runs on pushes to
`main` only, uses the `sourcemaps` environment, sets minimal `GITHUB_TOKEN`
permissions (`contents: read`; the store is reached only via
`SOURCEMAPS_TOKEN`), and sets `cancel-in-progress: false` so pushes queue rather
than aborting an upload or deletion midway (`.github/workflows/ci.yml` keeps
`true` and gains no secret or credentialed job). Deletion of old releases and
the real-credential check run automatically inside [#1819][i1819]'s upload job;
the maintainer runs nothing by hand.

- **Release naming and order:** one release per production build, tag and name
  `map-<UTC build time as YYYYMMDDTHHMMSSZ>-<full build SHA>` (workflow run
  start time, read once and passed to the command); maps and their index are its
  assets. Age and order are parsed strictly from the release name, never from
  GitHub's `created_at` (which follows the commit). The order is total: by
  parsed time, then by full SHA, as strings.
- **Uniqueness and protected entries:** before creating a release, the command
  looks for any release whose name ends with the same full SHA and, on a match
  (a rerun, even with a new run start time), reuses it and replaces its assets,
  so a SHA never has two releases. Drafts and prereleases are ignored; a name
  that does not parse or a `tag_name` that does not equal its release name is
  never deleted and is reported.
- **Retention (90-day floor, newest 10 protected):** **90 days provisionally**
  (2026-11-01), revisited at the first production release ([#2049][i2049]) so a
  map outlives its release plus an investigation margin. **90 days is a floor,
  not a deadline:** retention runs opportunistically only **after a successful
  upload** on a push to `main`. If `main` is not pushed after 90 days, old
  releases stay (push-time deletion with the newest 10 protected, not a strict
  automatic expiry).
- **Deletion and orphan tags:** after a successful upload, the job deletes
  published releases (ignoring drafts and prereleases) that are **both** older
  than 90 days and outside the newest 10 by the total order above, each release
  first by exact release ID and then its tag by exact tag ref after verifying
  the name parses, equals the tag, and matches the strict `map-` pattern (never
  by name lookup alone). A tag whose deletion failed is left and deleted by the
  next run, but only an **orphan tag** — a strict `map-` tag that no release of
  any kind (published, draft, prerelease, or one kept because its name does not
  parse or its name and tag disagree) references.
- **Failure handling:** a failed upload, refused/expired authentication, or
  failed release or tag deletion fails the job visibly; an upload failure skips
  retention and deletes nothing.

### Constraints: production gate, rotation and residual risk

- **Gate before the first production release ([#2049][i2049]):** [#2049][i2049]
  decides how maps of releases still in production are kept (marker, revised
  retention rule and post-retirement margin in this document, and tests in
  [#1819][i1819]'s command). No container image, application Dockerfile, release
  workflow or deployment is introduced before [#2049][i2049] is done.
- **Token rotation ([#2050][i2050]):** `SOURCEMAPS_TOKEN` expires about
  `2027-01-23`; the maintainer rotates it, guaranteed by existing reminders and
  an automated check rather than a promise: (1) [#2050][i2050] (assigned to the
  maintainer, due `2027-01-09`), which updates the secret, records the next
  expiry here and opens the next rotation issue; (2) two scheduled desktop
  reminders (`2027-01-09` and `2027-01-20`, set `2026-11-01`, running while the
  Claude app is open or on next launch); and (3) once [#1819][i1819] is merged,
  a check in its upload job (run **after** upload and retention so a near-expiry
  token still stores the current build's maps) that reads the
  `github-authentication-token-expiration` response header
  (`YYYY-MM-DD HH:MM:SS UTC`) and fails visibly when fewer than 14 days remain
  (missing or unparsable headers log a warning).
- **Residual risk:** a credentialed job merged into `main` can exfiltrate
  `SOURCEMAPS_TOKEN`; the `main`-only environment rule does not prevent that.
  Protection is independent review of every workflow and helper change
  ([third-AI review](../workflow/delivery.md#third-ai-review) of
  `.github/workflows/**`) and the maintainer's merge decision
  ([workflow rules](../workflow/README.md)).

[i1469]: https://github.com/tkakkie/ribbitto/issues/1469
[i1819]: https://github.com/tkakkie/ribbitto/issues/1819
[i2049]: https://github.com/tkakkie/ribbitto/issues/2049
[i2050]: https://github.com/tkakkie/ribbitto/issues/2050
[c1]: https://github.com/tkakkie/ribbitto/issues/1469#issuecomment-6403307193
[c2]: https://github.com/tkakkie/ribbitto/issues/1469#issuecomment-6403484872

