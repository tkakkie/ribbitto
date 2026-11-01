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
files. Production maps (#1819) follow
[decision 43](../decisions/43-typescript-with-a-go-driven-build.md)'s approved
private-storage policy once store details are fixed in issue #1469; match
maps and bundles by SHA, run id and content hash, recorded in the manifest.

In production, keep maps out of the image, embedded files and public static
routes; no inline maps or public `sourceMappingURL` link. Match a stack to the
exact output.
Verify CSP and delivery in the [target browsers](browser-targets.md), with
version-specific evidence under that document's verification rules. The
classic bundle needs a browser check; module/shared/lazy/preload delivery
needs verification before adoption, without loosening the nonce-only CSP.

