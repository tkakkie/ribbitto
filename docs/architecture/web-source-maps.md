# Web source maps

[Web toolchain](web-toolchain.md).

## Source maps — #1411

Development (`webbuild -dev` / `-watch`, #1821): `bin/dev-dist`: bundles,
external `<name>-<hash>.js.map` (`sourcesContent`,
`//# sourceMappingURL=<name>-<hash>.js.map`) and `manifest.json` `"map"` fields;
keep recent generations. Dev handler (`RIBBITTO_DEV_ASSETS=bin/dev-dist`):
registered bundles/maps served from disk, `Cache-Control: no-store`.

Production (`make bundles`): no maps, `sourceMappingURL` comments or `"map"` fields
in `web/static/dist`. Keep dev output outside embedded tree; its
handler never serves `.map`. Production maps (#1819, #1469,
[decision 43](../decisions/43-typescript-with-a-go-driven-build.md)): private
index beside maps outside `web/static`, never in `manifest.json`, matching
bundles by build SHA, run id and content hash.

Keep production maps out of images, embedded files and public static routes
(no inline maps/public `sourceMappingURL`).
Verify CSP/delivery in [target browsers](browser-targets.md): version-specific
evidence per its rules; classic-bundle browser check; verify module/shared/lazy/preload
before adoption, preserving nonce-only CSP.

## Private production store — #1469, #1819

[Decision 43](../decisions/43-typescript-with-a-go-driven-build.md): use
**private GitHub Release assets** in maintainer-only
`tkakkie/ribbitto-sourcemaps`: no external service; **never commit maps to Git**.
Actions artifacts do not fit public-repo CI because they cannot be private.

- **Setup:** private `tkakkie/ribbitto-sourcemaps` (README
  only, no collaborators/Pages; Issues/Wiki/Projects disabled). Only the
  maintainer reads/administers it; only CI writes. No AI holds the token or
  reads the store; AIs read only `tkakkie/ribbitto` runs via `gh` with their own
  token. Setup: (1) create repo; (2) create a maintainer 90-day fine-grained
  token for it only, **Contents: Read and write** (`Metadata: Read-only`
  automatic); (3) in `tkakkie/ribbitto` (`Settings → Environments`), create
  `sourcemaps` (`main` only), secret `SOURCEMAPS_TOKEN`. Never log/expose tokens.
- **Evidence:** `gh api repos/tkakkie/ribbitto/environments/sourcemaps` and
  `.../deployment-branch-policies` confirm the `main`-only environment.
  AI tokens get 403 on secrets and cannot
  inspect the store/token scope.
  Naming the private repo, token scope, permission and expiry is intentional.
  Never write a value, token id or private URL.

[Store](web-source-maps-store.md#storing-job-release-naming-and-retention--1819).

### Constraints

- **Gate ([#2049][i2049]):** closed until both parts merge: no container
  images, Dockerfiles, release workflows, tagged/distributed binaries (including
  `go install`), manual deploy or any distribution outside CI.
  Newest 10 protect almost nothing at AI merge pace; only the 90 days do.
  Require [verification](web-source-maps-store.md#exact-build-verification--2049-part-12)
  and #1819's first real store AND fetch record.
  No real production distribution/deployment until #2145 holds;
  [markers/retention/retirement margin](web-source-maps-store.md#production-markers--2049-part-22)
  are tested in #1819's `tools/sourcemaps` storing command.
- **Token rotation ([#2050][i2050]):** maintainer rotates `SOURCEMAPS_TOKEN`,
  expiring about `2027-01-23`. Best-effort reminders: desktop `2027-01-09`,
  `2027-01-20` (if Claude is open or next launches); assigned
  #2050 due `2027-01-09` (rotate secret, update expiry here, open next issue);
  #1819's job expiry check. Check **after** upload/retention so near-expiry
  tokens still store maps. Read `github-authentication-token-expiration`
  (`YYYY-MM-DD HH:MM:SS UTC`); fail visibly under 14 days remaining, warn on
  missing/unparsable headers. Only the failed Actions run is assured;
  notifications depend on pusher settings, hence reminders/#2050.
- **Residual risk:** `main` jobs automatically get the secret; `sourcemaps`
  enforces only `main`, no reviewer/wait timer. Any AI token can merge
  ([workflow rules](../workflow/README.md#what-github-enforces-and-what-is-only-a-rule));
  independent review and maintainer merge approval remain unenforced rules.
  Blast radius: read/delete/replace maps, write store repo, leak encoded token
  in logs. The maintainer may require an environment reviewer: a human gate per
  upload, against the no-manual-step decision.

[i2049]: https://github.com/tkakkie/ribbitto/issues/2049
[i2050]: https://github.com/tkakkie/ribbitto/issues/2050
