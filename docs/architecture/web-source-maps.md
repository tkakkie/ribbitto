# Web source maps

[Web toolchain](web-toolchain.md).

## Source maps — #1411

Development (`webbuild -dev` / `-watch`, #1821): `bin/dev-dist` holds bundles,
external `<name>-<hash>.js.map` (`sourcesContent`,
`//# sourceMappingURL=<name>-<hash>.js.map`) and `manifest.json` (`"map"` fields);
retain recent generations. `RIBBITTO_DEV_ASSETS=bin/dev-dist` serves registered
bundles/maps from disk with `Cache-Control: no-store`.

Production (`make bundles`): `web/static/dist` has no maps, `sourceMappingURL`
comments or `"map"` fields. Keep dev output outside the embedded tree, whose
handler never serves `.map`. Production maps (#1819, #1469,
[decision 43](../decisions/43-typescript-with-a-go-driven-build.md)) use a private
index beside maps outside `web/static`, never in `manifest.json`, matching
bundles by build SHA, run id and content hash.

Keep production maps out of images, embedded files and public static routes
(no inline maps/public `sourceMappingURL`).
Verify CSP/delivery in [target browsers](browser-targets.md) with version-specific
evidence under its rules: classic-bundle browser check; verify module/shared/lazy/preload
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
  inspect the store/token scope; setup/scope: #1469 [6403307193][c1], [6403484872][c2].
  Naming the private repo, token scope, permission and expiry is intentional.
  Never write a value, token id or private URL.

[Store](web-source-maps-store.md#storing-job-release-naming-and-retention--1819).

### Constraints

- **Gate before the first production release ([#2049][i2049]):** at AI merge pace,
  the newest 10 protect almost nothing; only the 90 days do. Require [verification](web-source-maps-store.md#exact-build-verification--2049-part-12)
  of exact SHA/run id/attempt/bundle hashes for every distributed/deployed build;
  [production markers, retention and retirement margin](web-source-maps-store.md#production-markers--2049-part-22)
  are tested in #1819's `tools/sourcemaps` storing command. Until both #2049 parts merge: no container
  images, Dockerfiles, release workflows, tagged/distributed binaries (including
  `go install`), manual deployment or other build distribution outside CI.
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
[c1]: https://github.com/tkakkie/ribbitto/issues/1469#issuecomment-6403307193
[c2]: https://github.com/tkakkie/ribbitto/issues/1469#issuecomment-6403484872
