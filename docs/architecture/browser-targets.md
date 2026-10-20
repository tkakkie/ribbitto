# Target browsers

Which browsers ribbitto supports, the design rules for browser
compatibility and fallbacks, and what is currently verified. Decided by the
maintainer on 2026-10-20 (#1406).

## Supported browsers and versions

- Desktop Chrome, Edge, Firefox and Safari: the latest two major versions each.
- iOS / iPadOS Safari: the latest two generations of iOS / iPadOS.
- Android Chrome: the latest two major versions.
- The target is the latest patch of each official stable release. Beta and Nightly builds, older browsers, embedded WebViews and all other browsers are best effort.

## Design rules

1. **HTML and CSS first.** Use native HTML elements and CSS before
   reaching for JavaScript.
2. **Check compatibility and provide a fallback.** Before using a Web API
   or CSS feature that some target browser lacks, check compatibility
   across the target browsers and provide a fallback where needed (a
   script fallback, or not using the feature).
3. **The core keeps working with JavaScript off.** Core functionality
   works without JavaScript, as settled in
   [decision 2](../decisions/02-server-rendered-html-with-htmx-not-an-spa.md),
   [decision 19](../decisions/19-web-layers-server-owned-html-htmx-swaps-javascript-as-enhancement.md)
   and
   [decision 40](../decisions/40-plain-javascript-by-default-library-islands-by-measured-exception.md)
   ([`web-layers.md`](web-layers.md#progressive-enhancement)), and no core
   fallback may depend on JavaScript alone.
4. **Build target agreement.** The build target chosen by #1356 (pending;
   not decided here) must agree with this document.
5. ***Supported* is not *tested*.** This document records, per browser,
   what is verified and what is not; a target browser that has not been
   verified is recorded as unverified, never as passing.

## Verification status

Only what the repository's checks run is recorded as verified; a check counts
for a target release only when it records the browser version, and everything
else is recorded as unverified until a check exists.

| Browser | Target versions | Verified how | Status |
|---|---|---|---|
| Desktop Chrome | latest two major versions | headless Chrome via go-rod in `TestBrowser` (`internal/web`) and `TestTopicPostBrowser` (`cmd/ribbitto`) with `RIBBITTO_REQUIRE_BROWSER=1` ([`web-layers-checks.md`](web-layers-checks.md#shared-browser-tests)), running on the Chrome installed on the CI runner image, whose version CI neither pins nor records; older major version not verified | unverified (target versions are unverified for now: the tests run on some Chrome release, but which major and patch version, and whether it is the latest stable release, is not recorded; becomes verified only once a check records the concrete version, planned in #1426) |
| Desktop Edge | latest two major versions | not verified | unverified |
| Desktop Firefox | latest two major versions | not verified | unverified |
| Desktop Safari | latest two major versions | not verified | unverified |
| iOS / iPadOS Safari | latest two generations of iOS / iPadOS | not verified | unverified |
| Android Chrome | latest two major versions | not verified (smoke test planned in #1426) | unverified |
