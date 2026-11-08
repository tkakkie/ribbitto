# M9: Front-end foundation and UX

## Outcomes

1. **Typed, unit-tested client code.** Client code is typed and its logic unit-tested; no untyped script can be added.
2. **Shared DOM contract and hashed delivery.** Scripts and templates share one DOM contract generated from Go; scripts are delivered under content-hashed names.
3. **Written conventions and browser targets.** Front-end conventions and target browsers are written down.
4. **Opening a conversation stays cheap.** The cost of opening a conversation stays bounded as messages and features grow: a smaller first render (#1477, with shown-interval read state, #1486, and permalinks to recent messages that still land on a page showing them), the phone first-paint fix (#1480), smaller per-message markup (#1466's F-series with #1455 and #1457) and a standing per-message budget check in CI (#1478).
5. **Unread indicators on the tab.** Unread counts reach the tab title and favicon.

## Status: done with exceptions

[Final decision](https://github.com/tkakkie/ribbitto/issues/1426#issuecomment-6716626894): #1426/#1404/M9 stay open pending separate maintainer approval.

- R3: **77 PASS / 4 FAIL**, unchanged after A/B1/C.
- R4 levels 1–2: Chrome go-rod PASS; Playwright WebKit 17/17 PASS, not iOS Safari.
- R5: no open serious finding.
- #2180: **merged; final review done** (PR #2196, #2219 record); production release needs approval.

### Exceptions

R3 failures stay FAIL at `33d709a`.

- **E-1 — L4b/L4c phone feed (send→visible/composer enabled):** B1: slower head unreproduced; both group ~25/45 ms. Unknown cause; server/network/1–2 ms effect possible. No B2/new issue; revisit: slow-send reports, first post-M9 composer/send change, real-device run.
- **E-2 — L6 desktop feed-ime p90:** C unreproduced; both commits slow in headless Chrome, absent that night headed. Headless cause unknown. No more runs/new issue. Revisit: typing lag, composer input changes, optional headed repeat.
- **E-3 — L3 phone feed first load:** 0.5 ms over limit; median **8.0 ms slower (74.6 → 82.6 ms)**. Head slower all four sessions (+17.1/+2.5/+4.4/+9.5 ms); no-slowdown unproven; no L3 rerun. p90 within limit (111.0 → 112.4 ms), other first-load cells pass: M9 nonblocking. #2262 (M10): alternate base `fdf5943`/head after method/cap approval; no measurement to close M9.
- **E-4:** `sourcemaps-store` at `33d709a`: GitHub API 500 creating draft release, run 42061990718. **No source maps for `33d709a`**; later runs succeeded.

### Unmet/unverified

- X-1: R4 levels 3–4: **NOT RUN**: real iOS Safari/Android Chrome (device/emulator); keyboard/viewport/bfcache/device first-paint frequency unverified. M10 #2261.
- X-2: network-delay profile: **NOT RUN**, approved; slow-link switching, own/others' posts, first load unmeasured. #1408.
- X-3: phone first paint: **KNOWN DEFECT, DEFERRED**: known pre-jump oldest-message flash; device frequency **NOT VERIFIED**. #1480; #2261 samples.
- X-4: shown-interval read state: **DEFERRED / KNOWN LIMIT**: S2–S5/S4b newest-page opening can mark unseen messages read. #1486; #2219 trigger.
- X-5: 30-message first render / A-server permalinks: **DEFERRED**, still 50. #2165, #2219.
- X-6: switching slower than before M7: #1453: own E1 **FAIL**, E2 **NOT VERIFIED**; not an input to L1/L2.
- X-7: R1 rows 17/19/20: Rapid-switch/focus/announcement **NOT VERIFIED** in Chrome; WebKit passed each. VoiceOver/NVDA/TalkBack **NOT RUN**. #2261 where possible; screen readers need a person.
- X-8: R1 row 22 / A3: Real-server `streams.open` **NOT VERIFIED**; check below. Maintainer decides sufficiency/carry-over.
- X-9: R5 B5/B7: Accepted moderate: read-state fragmentation cost; topic-count filtering/first-unread lookup bounded only by `statement_timeout`. Production load untested; revisit on trouble.
- X-10: R5 minor: B2a guard coverage; A11 vendor libraries outside Dependabot; A1 Node tarball unhashed; nightly `tools/tbls` `vuln` advisories addressed after head (#2232). No follow-up.

X-8 (§4): `33d709a`, disposable PostgreSQL 18, local `RIBBITTO_DEV_METRICS_ADDR`, Playwright Chrome 157.0.8103.39, one member/tab. 0.5/2/5 s readings: 1 after open, six overlapping-navigation rounds, timed `window.stop()`/CDP `Page.stopLoading`, five rapid Back/Forward pairs, each ending loaded; closed: 0. 15 ms sampled peak: 1. Mid-stream interruption unconfirmed; shorter overlaps may be missed. No second tab/real network/WebKit; long-lived stray connection possible.

## Position in the roadmap

Confirmed by the maintainer (2026-10-19): after M7 (the composer track),
M9's foundation (phases 1–5) starts first and runs alongside the *Basics*
track (private channels → direct messages → invitations). *Basics* does not
wait for M9 as a whole, and M9's UX items (phase 6) do not block *Basics*.
Where both change the same code, the two issues state their dependency and
order before either is made `ready`; in particular, if #1361's A1 is
revisited, private channels and the conversation cache share one access
model (#1361), and whichever lands second adds the tests for the other.

Swap/cache deferred; partial navigation: #1408, new design/measurement, explicit approval.

## Out of scope

- **Mention notifications (#1423):** a follow-up after M9, listed with Web
  Push in [After the MVP](roadmap.md#after-the-mvp); they need their own
  server-side supply and authorization, while the direction stays in pending
  decision #1361.
- **Later features (#1361 A2, A3 and B):** reactions, editing, deleting,
  uploads, emoji picker and quick switcher; pending decision #1361 records
  their client-side design rules, and they are built in their own
  milestones.
- **Not planned:** rendering the message list on the client, and native apps
  ([Not planned](roadmap.md#not-planned)).
