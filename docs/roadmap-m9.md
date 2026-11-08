# M9: Front-end foundation and UX

## Status: done with exceptions

[Final decision record](https://github.com/tkakkie/ribbitto/issues/1426#issuecomment-6716626894):
**M9: done with exceptions**. [#1426],
[#1404] and M9 stay open until
the maintainer's separate approval. Criteria remain in [#1404].

- [R3](https://github.com/tkakkie/ribbitto/issues/1426#issuecomment-6703215809):
  **77 PASS / 4 FAIL**, unchanged by
  [follow-up A/B1/C](https://github.com/tkakkie/ribbitto/issues/1426#issuecomment-6715170652).
- [R4 levels 1–2](https://github.com/tkakkie/ribbitto/issues/1426#issuecomment-6706152731):
  Chrome go-rod PASS; Playwright WebKit 17/17 PASS, not iOS Safari.
  [Scope and counts](architecture/browser-targets.md#verification-status).
- [R5](https://github.com/tkakkie/ribbitto/issues/1426#issuecomment-6706153056):
  no open serious finding.
- [#2180]: **merged; final review done**
  ([PR #2196 final review](https://github.com/tkakkie/ribbitto/pull/2196),
  [#2219] record);
  production release needs separate approval.

### Explicit exceptions

R3 failures stay FAIL at `33d709a`.

- **E-1 — L4b/L4c, phone feed (send to visible/composer enabled):**
  B1 did not reproduce the slower head;
  both commits show groups around 25/45 ms. Cause unknown;
  server/network influence and a 1–2 ms effect are not excluded. No B2 or
  new issue. Revisit on slow sending reports, the first composer/send-path
  change after M9, or a real-device run.
- **E-2 — L6, desktop feed-ime p90:** C did not reproduce it; both commits
  show the slow state in headless Chrome, absent in the night's headed runs.
  Headless Chrome as cause is unproven. No more measurement or
  new issue; revisit on typing-lag reports, composer input changes, or a
  headed repeat if wanted.
- **E-3 — L3, phone feed first load:** limit exceeded by 0.5 ms, but median
  **8.0 ms slower (74.6 → 82.6 ms)**. Head is slower in all four sessions
  (+17.1/+2.5/+4.4/+9.5 ms), so no-slowdown cannot be concluded; no L3
  follow-up run was made. p90 is inside the limit (111.0 → 112.4 ms) and
  other first-load cells pass, so this is not treated as blocking M9.
  Handed to [#2262] (M10):
  alternate base `fdf5943` and head, method and cap approved first.
  Nothing is measured to close M9.
- **E-4:** `sourcemaps-store` failed on `33d709a`, GitHub API 500 creating
  the draft release, [run 42061990718](https://github.com/tkakkie/ribbitto/actions/runs/42061990718).
  **No source maps stored for `33d709a`**; later runs succeeded.

### Items not met or not verified

| Item | Status and follow-up |
|---|---|
| X-1: R4 levels 3–4 | **NOT RUN**: real iOS Safari / Android Chrome (device or emulator); keyboard, viewport, bfcache and device first-paint frequency unverified. M10 [#2261]. |
| X-2: network-delay profile | **NOT RUN**, approved exception; slow-link switching, own/others' posts and first load unmeasured. [#1408]. |
| X-3: phone first paint | **KNOWN DEFECT, DEFERRED**: oldest messages can flash before the jump; mechanism known, device frequency **NOT VERIFIED**. [#1480], [#2261] sampling. |
| X-4: shown-interval read state | **DEFERRED / KNOWN LIMIT**: S2–S5/S4b; newest-page opening can mark never-shown messages read. [#1486], [#2219] trigger. |
| X-5: 30-message first render / A-server permalinks | **DEFERRED**, first render stays 50. [#2165], [#2219]. |
| X-6: switching slower than before M7 | [#1453]: own E1 **FAIL**, E2 **NOT VERIFIED**; not an input to L1/L2. |
| X-7: R1 rows 17/19/20 | Rapid switches/focus/announcement **NOT VERIFIED** in Chrome tests; WebKit step passed each. VoiceOver/NVDA/TalkBack **NOT RUN**. [#2261] where device allows; screen readers need a person. |
| X-8: R1 row 22 / A3 | Real-server `streams.open` **NOT VERIFIED**, unchanged by minimal check below. Maintainer decides sufficiency or carrying the exception. |
| X-9: R5 B5/B7 | Accepted moderate limits: read-state fragmentation cost; unbounded topic-count filtering/first-unread lookup, bounded only by `statement_timeout`. Production load unexercised; revisit if it shows a problem. |
| X-10: R5 minor | B2a guard coverage; A11 vendor libraries outside Dependabot; A1 Node tarball bytes not hashed; nightly `vuln` advisories in `tools/tbls` addressed after head ([#2232]). No follow-up required. |

X-8 check (final record §4): `33d709a`, disposable PostgreSQL 18,
local server with `RIBBITTO_DEV_METRICS_ADDR`, Chrome 157.0.8103.39 via
Playwright, one member/tab. Readings at 0.5/2/5 s were 1 after opening,
six overlapping-navigation rounds, timed `window.stop()` and CDP
`Page.stopLoading` interruptions, and five rapid Back/Forward pairs, each
then a loaded page; closing the page gave 0. Peak sampled every
15 ms was 1: mid-stream interruption was not confirmed, shorter overlaps
could be missed. No second tab, real network or WebKit; a long-lived stray
connection is not ruled out. Row 22 stays **NOT VERIFIED**.

## Position in the roadmap

M9 does not block *Basics*; M10 follows ([roadmap](roadmap.md#after-the-mvp)).
Swap/cache remain deferred; partial navigation needs [#1408], a new
design/measurement and explicit approval.

[#1404]: https://github.com/tkakkie/ribbitto/issues/1404
[#1408]: https://github.com/tkakkie/ribbitto/issues/1408
[#1426]: https://github.com/tkakkie/ribbitto/issues/1426
[#1453]: https://github.com/tkakkie/ribbitto/issues/1453
[#1480]: https://github.com/tkakkie/ribbitto/issues/1480
[#1486]: https://github.com/tkakkie/ribbitto/issues/1486
[#2165]: https://github.com/tkakkie/ribbitto/issues/2165
[#2180]: https://github.com/tkakkie/ribbitto/issues/2180
[#2219]: https://github.com/tkakkie/ribbitto/issues/2219
[#2232]: https://github.com/tkakkie/ribbitto/issues/2232
[#2261]: https://github.com/tkakkie/ribbitto/issues/2261
[#2262]: https://github.com/tkakkie/ribbitto/issues/2262
