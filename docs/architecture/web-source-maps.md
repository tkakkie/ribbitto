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

See [storing job, release naming and retention](web-source-maps-store.md#storing-job-release-naming-and-retention--1819).

### Constraints: production gate, rotation and residual risk

- **Gate before the first production release ([#2049][i2049]):** "the newest 10"
  protects almost nothing at AI merge pace, only the 90 days do. #2049 ensures a
  release exists for the exact SHA, run id, attempt and bundle hashes of every
  distributed or deployed build ([verification](web-source-maps-store.md#exact-build-verification--2049-part-12)) and defines
  how production releases' maps are kept (marker, revised retention rule and
  post-retirement margin in [the store page](web-source-maps-store.md#storing-job-release-naming-and-retention--1819), and tests in #1819's command). No container
  image, Dockerfile, release workflow, tagged or distributed binary (including
  `go install`), manual deployment or other build distribution outside CI is
  allowed until both parts of #2049 have merged.
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

[i2049]: https://github.com/tkakkie/ribbitto/issues/2049
[i2050]: https://github.com/tkakkie/ribbitto/issues/2050
[c1]: https://github.com/tkakkie/ribbitto/issues/1469#issuecomment-6403307193
[c2]: https://github.com/tkakkie/ribbitto/issues/1469#issuecomment-6403484872
