# Web source maps (planned)

Source-map storage and delivery rules for the [web toolchain](web-toolchain.md).

## Source maps — #1411

Serve development maps locally. Production maps follow
[decision 43](../decisions/43-typescript-with-a-go-driven-build.md)'s approved
private-storage policy; store details are fixed in issue #1469, before #1411. Match maps/bundles by SHA, run id and
content hash; the manifest records SHA/run id.

**Map contents — confirmed:** include `sourcesContent`, once the private store
of #1469 is decided.

Keep maps out of the image, embedded files and public static routes; no inline
maps or public `sourceMappingURL` link. Match a stack to the exact output.
Verify CSP and delivery in the [target browsers](browser-targets.md), with
version-specific evidence under that document's verification rules. The
classic bundle needs a browser check; module/shared/lazy/preload delivery
needs verification before adoption, without loosening the nonce-only CSP.

