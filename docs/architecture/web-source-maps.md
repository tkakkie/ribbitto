# Web source maps

Source-map storage and delivery rules for the [web toolchain](web-toolchain.md).

## Source maps — #1411

In development (`webbuild -dev` and `webbuild -watch`, #1821), each bundle has
an external `<name>-<hash>.js.map` file with `sourcesContent` and a
`//# sourceMappingURL=<name>-<hash>.js.map` comment, and `dist/manifest.json`
records the map filename in each entry's `"map"` field. With
`RIBBITTO_DEV_ASSETS=web/static`, the static handler serves registered bundles
and `.js.map` files from disk with `Cache-Control: no-store`.

Production builds (`make bundles`) omit source maps, `sourceMappingURL`
comments and `"map"` manifest fields, and replace `web/static/dist` as a whole
so a prior development build cannot leak into the embedded production tree. The
embedded production handler never serves `.map` files. Production maps (#1819)
follow
[decision 43](../decisions/43-typescript-with-a-go-driven-build.md)'s approved
private-storage policy once store details are fixed in issue #1469; match
maps and bundles by SHA, run id and content hash, recorded in the manifest.

Keep maps out of the image, embedded files and public static routes; no inline
maps or public `sourceMappingURL` link. Match a stack to the exact output.
Verify CSP and delivery in the [target browsers](browser-targets.md), with
version-specific evidence under that document's verification rules. The
classic bundle needs a browser check; module/shared/lazy/preload delivery
needs verification before adoption, without loosening the nonce-only CSP.

