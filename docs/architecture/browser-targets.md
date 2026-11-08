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
4. **Build target.** The classic IIFE bundles built by `tools/webbuild`
   (#1411) use ES2018. The maintainer raised the syntax target from ES2017 on
   2026-11-03 (#2119): supported browsers run ES2018, so lowering object
   spread/rest adds per-bundle helpers without useful compatibility.
   This syntax floor does not complete the browser verification still owed in
   the status table below; #1820 supplies the served-bundle check and #1426
   defines the verification levels.
   It provides no Web API polyfills; rule 2 still applies API by API.
   A later target change needs a recorded reason and must keep this document
   and [`web-toolchain.md`](web-toolchain.md) in agreement.
5. ***Supported* is not *tested*.** This document records, per browser,
   what is verified and what is not; a target browser that has not been
   verified is recorded as unverified, never as passing.

## Verification status

Both automated tests and manual checks count as verification evidence;
anything without evidence is recorded as unverified.

Verification requires browser name, concrete major/patch version (OS/device version
where relevant), date, environment (device/emulator/simulator/automation), scope,
result and evidence link/pointer (CI job, test, review or issue comment).
A missing field means unverified. Each target version needs its own record;
one version never verifies a family or the latest two. Environments need separate
records: Playwright WebKit is not real iOS Safari; emulators are not devices.

| Browser | Target versions | Verified how | Status |
|---|---|---|---|
| Desktop Chrome | latest two major versions | R4 level 1, Chrome 157.0.8103.39 via go-rod; see run record below; every CI run records the Chrome version, shard, date and runner image in each browser job's step summary | PASS for the recorded scope/version; latest two major versions remain unverified |
| Desktop Edge | latest two major versions | not verified | unverified |
| Desktop Firefox | latest two major versions | not verified | unverified |
| Desktop Safari | latest two major versions | not verified | unverified |
| iOS / iPadOS Safari | latest two generations of iOS / iPadOS | R4 level 3: real-device checklist in [#2261] (M10) | NOT RUN |
| Android Chrome | latest two major versions | R4 level 4: device or emulator checklist in [#2261] (M10) | NOT RUN |

[R4 run record](https://github.com/tkakkie/ribbitto/issues/1426#issuecomment-6706152731)
(2026-11-07, head `33d709a`):

- **Level 1 — Chrome go-rod:** Chrome 157.0.8103.39, clean head extract,
  `webbuild` bundles, `RIBBITTO_REQUIRE_BROWSER=1`. `TestBrowser` without DB:
  1157 PASS, 0 FAIL, 59 SKIP (PostgreSQL required); DB scenarios on disposable
  PostgreSQL 18: 58 PASS, 0 FAIL, 0 SKIP. `cmd/ribbitto` with
  `RIBBITTO_REQUIRE_DB=1`: 73 PASS, 0 FAIL, 0 SKIP.
- **Level 2 — Playwright WebKit:** 1.64.1 WebKit build, local TLS proxy to
  head server: **17/17 PASS**. Scope: CSP, switching/title/focus/status,
  history/one stream request, overlapping navigation, drafts, own/live posts,
  offline recovery, tab unread/title/favicon, older messages, JavaScript-off
  reading/posting/paging/switching. **WebKit, not iOS Safari**; engine version
  unrecorded, so no supported Safari version verified.
- **Levels 3–4 — real iOS Safari / Android Chrome:** **NOT RUN**; [#2261].
  Device keyboard/viewport/bfcache unverified; WebKit/desktop Chrome cannot substitute.

[#2261]: https://github.com/tkakkie/ribbitto/issues/2261
