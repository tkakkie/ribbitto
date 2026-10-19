# Measurement Go image

The `image` that the [build routes](investigations.md#build-routes) of a host
measurement use to cross-compile is:

`golang@sha256:820153096fda4de6ac1b7194d12e416b7dd8f4e9a33d17e0345b111b945162d8`

It is the official `golang:1.27.2` image (Go 1.27.2 linux/arm64). Pull it by
digest, never by tag.

## Approval

The maintainer approved the digest that the official `golang:1.27.2` tag
resolved to when it was pulled on 2026-10-19, as trusted for the #1162 and
#1177 measurements. The result is in the
[measurement comment](https://github.com/tkakkie/ribbitto/pull/1352#issuecomment-6263271044).

## Replacing it

Replace the digest, with the maintainer's approval, when go.mod's `go`
version outgrows it. The previous local image (Go 1.27.1) could not build
SHAs whose go.mod says 1.27.2 with `GOTOOLCHAIN=local`.
