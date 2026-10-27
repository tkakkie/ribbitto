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
4. **Build target.** The initial classic IIFE bundles built by `tools/webbuild`
   (#1411) use ES2017 as the syntax target, approved by the maintainer on
   2026-10-26 because its syntax is conservative for the latest-two target
   browsers and iOS generations listed above.
   This is a syntax floor only: browser verification stays in the status table
   below and the browser check of the served bundle delivered by #1820, with #1426
   defining the verification levels.
   It provides no Web API polyfills; rule 2 still applies API by API.
   A later target change needs a recorded reason and must keep this document
   and [`web-toolchain.md`](web-toolchain.md) in agreement.
5. ***Supported* is not *tested*.** This document records, per browser,
   what is verified and what is not; a target browser that has not been
   verified is recorded as unverified, never as passing.

## Verification status

Both automated tests and manual checks count as verification evidence;
anything without evidence is recorded as unverified.

A record counts as verified only when it records all of the following (a
record missing any of them is not a verification):

- **Browser name.**
- **Concrete version:** major and patch, and the OS or device version where
  relevant (for example iOS / iPadOS and Safari).
- **Date.**
- **Environment:** real device, emulator or simulator, automated test, and
  so on.
- **Scope of the check:** what was exercised.
- **Result.**
- **Evidence:** a link or a pointer to where the run is recorded (for
  example a CI job, a test name, or the review or issue comment).

A result for one version counts for that version only, never for the whole
browser family and never for "the latest two versions"; each target version
is verified, or recorded unverified, on its own. A different environment
(for example Playwright's WebKit versus real iOS Safari, or an emulator
versus a device) is a different record: WebKit is not iOS Safari.

| Browser | Target versions | Verified how | Status |
|---|---|---|---|
| Desktop Chrome | latest two major versions | headless Chrome via go-rod in `TestBrowser` (`internal/web`) and `TestTopicPostBrowser` (`cmd/ribbitto`) with `RIBBITTO_REQUIRE_BROWSER=1` ([`web-layers-browser-tests.md`](web-layers-browser-tests.md#shared-browser-tests)), running on the Chrome installed on the CI runner image, whose version CI neither pins nor records; older major version not verified | unverified (target versions are unverified for now: the tests run on some Chrome release, but which major and patch version, and whether it is the latest stable release, is not recorded; a target version becomes verified only when a check records all required fields, including the concrete version, planned in #1426) |
| Desktop Edge | latest two major versions | not verified | unverified |
| Desktop Firefox | latest two major versions | not verified | unverified |
| Desktop Safari | latest two major versions | not verified | unverified |
| iOS / iPadOS Safari | latest two generations of iOS / iPadOS | not verified (manual check on a real iOS device planned in #1426; see the planned levels below) | unverified |
| Android Chrome | latest two major versions | not verified (check on a real Android device or an emulator planned in #1426; a real device and an emulator are different records, each recorded as what it is) | unverified |

[#1426](https://github.com/tkakkie/ribbitto/issues/1426) plans four
separately recorded verification levels: the existing Chrome go-rod tests,
a WebKit run recorded as WebKit and never as iOS Safari, a real iOS device
run, and an Android Chrome run (on a real device or an emulator, each
recorded as what it is).
