# Measurement Go image

The `image` that the [build routes](investigations.md#build-routes) of a host
measurement use to cross-compile is:

`golang@sha256:6928d94d1142af58816fed5a62338d605a266ccd53a0deaab6b1096f19059261`

It is the official `golang:1.27.3` image (Go 1.27.3 linux/arm64). Pull it by
digest, never by tag.

The digest is the index digest of the official tag, looked up on 2026-10-21
with `docker buildx imagetools inspect golang:1.27.3` (manifest metadata
only; it lists linux/arm64 and linux/amd64 manifests).

## Approval

The maintainer approved fixing the vulnerable toolchain first on
[#1492](https://github.com/tkakkie/ribbitto/issues/1492) on 2026-10-21
(GO-2026-6809), and the `golang:1.27.3` digest is approved in writing on that
issue.

Earlier pin `golang@sha256:820153096fda4de6ac1b7194d12e416b7dd8f4e9a33d17e0345b111b945162d8`
(`golang:1.27.2`): the maintainer approved the digest that the official tag
resolved to when it was pulled on 2026-10-19, as trusted for the #1162 and
#1177 measurements. The results are in the comments on
[#1162](https://github.com/tkakkie/ribbitto/pull/1352#issuecomment-6263271044) and
[#1177](https://github.com/tkakkie/ribbitto/pull/1376#issuecomment-6263271467).

## Replacing it

Replace the digest, with the maintainer's approval, when go.mod's `go`
version outgrows it: the image's Go version must be at least the `go` line of
the commit under test, because `GOTOOLCHAIN=local` refuses to build otherwise.
