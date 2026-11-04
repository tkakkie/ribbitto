# Design and conventions review — 2026-11-04

Audit of `main` at `e79d7f17` (all `file:line` references are at that commit).
Read-only review: six parallel audits (import rules vs checks, domain modules,
web layer, tests, tooling/CI/workflow, docs and comments), with key claims
re-checked by hand.

**This file is a working brief, not a repository document.** Do not merge it;
turn each work item into an issue/PR and delete the file when done.

## How to use this brief (for the implementing AI)

- Read `AGENTS.md` and the docs of the area before each item; follow
  `docs/workflow/` for branches (`claude/<topic>`), PR size (~400 lines) and review.
- Items are tagged **VERIFIED** (re-checked by reading code) or **SUSPECTED**
  (evidence seen, effect not reproduced). For SUSPECTED items, reproduce or
  confirm first; drop the item if it does not hold.
- Line numbers may have drifted; search by identifier if they do not match.
- Items that change a decision (26/K7, 29, 46) or a workflow rule need the
  maintainer's approval in an issue first. They are marked **NEEDS DECISION**.

## Summary

1. The bottleneck is the growth of rules and tooling, not the code.
   Process+docs commits: 33% (late Sep) → 38% (Oct) → 55% (Nov 1–4).
   `cmd/argonreview` grew to ~66k lines in ~2.5 weeks and was deleted in
   `9c9ffb13` (67,901 deletions) — about 4× the product's production code.
2. Workflow principle 4 ("rules grow only from incidents; a rule that never
   prevented anything goes") is not applied: ~115 workflow rules, 4 incident links.
3. The code design is sound. Fix small duplications and stale docs; no rewrite.

Size baseline: production Go ~17.7k lines (excluding generated), tests ~62k,
`tools/` ~17.9k (prod+test), hand-written `docs/` ~12k (excluding generated
files and decisions; `docs/images/screenshot.webp` inflates `wc` by 677).

---

## PR plan (suggested order)

| PR | Content | Size | Needs decision |
|---|---|---|---|
| 1 | Fix false doc claims (§1), remove unused idiomorph (W1), AGENTS.md under 100 lines (§6.1) | small | AGENTS.md text: maintainer review |
| 2 | Code dedup: `validateText` (D6), not-found helper (W2), 429 helper (W5), Makefile duplicate lines (T5) | small–medium | no |
| 3 | Remove `typedscripts`, `contractliterals`, `measurementskip` (T2) | ~1.2k deleted | yes (tool removal) |
| 4 | Testing policy (§6.3) + delete duplicated browser cases (Q1), rename drafts browser test (Q2) | ~1.5k deleted | yes (policy) |
| 5+ | Docs cleanup by area (§5 table), largest first | large, split | no (decision 46 already covers it) |
| later | Runner unification (D1), shared ports (D2), `channelPages` split (W4), browser harness (Q3) | medium | D2, Q3: yes |

---

## 1. False or stale documentation claims

All VERIFIED unless noted.

| Doc claim | Reality |
|---|---|
| `docs/architecture/packages.md:83` diagram: `platform --> kernel` | `internal/platform/postgres` does not import `kernel`. Diagram also omits `cmd/ribbitto` → conversation, org, kernel (see `docs/dependencies.md:23,27,28`). Better: delete the diagram (duplicates generated `docs/dependencies.md`). |
| `docs/architecture/modules.md:39`: only conversation page counts and sidebar recounts use the statement-budget snapshot | `internal/realtime/internal/postgres/cleaner.go:39` also calls `InSnapshotWithStatementTimeout`. |
| `docs/architecture/unread-scope-writes.md:17,40,50`: shown runs are "dormant", "No HTTP caller exists" | `internal/web/read.go:54,56` calls `TopicShown`/`FeedShown` from `POST …/read`. True part: no client script sends `from`/`through`. |
| `docs/architecture/README.md:50-52,54` marks web toolchain, conventions, source maps, checks as *planned* | All built (`web/src/*.ts`, `tools/typedscripts`, `tools/sourcemaps`, empty `web/legacy-scripts.txt`). |
| `docs/architecture/web-conventions.md:1` and `web-toolchain-checks.md:1` titled "(planned)" | Implemented. |
| `docs/architecture/web-source-maps.md:52` "Gate: closed until both parts merge" | Both merged: #2131 (`9bd43105`), #2137 (`2d5917c6`). |
| `docs/architecture/web-toolchain-checks.md:13-15` "no `localStorage` allowance; #1418 adds…" | `web/static/javascript_test.go:111-113` exempts `message-drafts.ts`. |
| `docs/architecture/web-layers.md:30-31` components never receive module results | `internal/web/view/typing.templ:15` takes `typing.Snapshot`; depguard `view` rule (`.golangci.yml:169-184`) does not deny typing/presence/realtime/unread. Decide: fix code or the rule. |
| `docs/architecture/web-conventions.md`: a script-only value "is never put in the DOM" | `data-unconfirmed-key` is written and read only by `web/src/composer.ts` (81, 95, 605, 612, 614), yet documented as a contract in `web-contracts-generated.md:22`. |
| `docs/architecture/features.md:37-39` other features' tests write tables only via the owner's fixtures | Direct SQL: `internal/conversation/conversationpg/post_test.go:31` (`UPDATE organization`), `internal/realtime/internal/postgres/appender_test.go:29`, `internal/web/org_test.go:586` (`UPDATE channel`), `internal/org/internal/postgres/schema_test.go:61` (`INSERT INTO event_log`). Decision 29 allows "scenario-specific SQL" — reword the doc to match the decision. |
| `AGENTS.md:67` "except flows the feature map lists" implies foreign SQL writes | `tools/tablecheck` allows no foreign query writes (`tools/tablecheck/table_test.go:306`: "Cross-module flows use injected APIs"). |
| `AGENTS.md:45-46` depguard keeps the non-module rules | The manifest also checks compose (`module_imports_test.go:72,96-106`). |
| `docs/architecture/import-checks.md:9-10` manifest rejects unlisted module-shaped dirs | Only dirs with store/wiring/fixture children are found (`module_imports_test.go:197-209`); a new memory-only module would go unnoticed. |
| `docs/architecture/import-checks.md:13-15` vs `:34` | Self-contradiction on what is enforced. |
| `docs/architecture/modules.md:16` wiring imports only its root and store | Enforced exactly only for `identitypg` (`exactWiringStore`, `module_imports_test.go:26,126-127`). |
| `docs/architecture/modules.md` "stores never open, commit or roll back" | `internal/identity/internal/postgres/session.go:34`, `internal/realtime/internal/postgres/reader.go:37`, `cleaner.go:39,50` open their own transactions. Align code or document single-module exception. |
| `docs/architecture/features.md:18` lists each feature's files | Omits `web/src/message-menu.ts`, `composer-queue.ts`, `popover-focus.ts`, `arrival.ts`. Better: delete the file lists. |
| SUSPECTED: `docs/roadmap.md:34`, `roadmap-m9.md:66-69`, `roadmap-m10.md` list pending M9 conversions | #2130 emptied the legacy list. |

---

## 2. Domain modules (`internal/{identity,org,conversation,realtime,unread,presence,typing,compose,platform}`)

Production code declares ~82 interfaces and ~30 named factory func types.

- **D1. Transaction runner seam duplicated per module.** VERIFIED.
  `TxRunner` at `conversation/ports.go:38`, `org/registration.go:18`,
  `unread/reading.go:12`; `SnapshotRunner` at `conversation/ports.go:121`,
  `org/member_search.go:34`. Implementations copied in
  `conversationpg/conversationpg.go:77-102`, `orgpg/orgpg.go:64-79`,
  `unreadpg/wiring.go:20-28`; `commitLimit = 5s` duplicated
  (`conversationpg.go:73`, `unreadpg/wiring.go:17`).
  Fix: interfaces + `DetachedRunner{Pool, Limit}` in `platform/postgres`.
  Also: org setup/sign-up use plain `InTx`, not the detached commit — decide
  whether that is intended. ~70 lines, low risk.
- **D2. Identical consumer-owned ports force `compose` adapters.** VERIFIED.
  `EventAppender` (`conversation/ports.go:105`, `org/event_appender.go:12`),
  `EventCursor` (`conversation/ports.go:171`, `unread/feed.go:41`),
  `GenerationRaiser` (`presence/presence.go:67`, `typing/state.go:26`).
  Wiring returns concrete store types, so `compose/adapters.go:13-39` adapts
  each. Letting the provider root own these few shared ports removes most of
  `adapters.go`. ~80–120 lines. **NEEDS DECISION** (decision 26 / K7 on #502).
- **D3. unread single-implementation seams.** VERIFIED.
  `PageChannelCounts`/`PageTopicCounts` + `func() X` factories
  (`unread/page.go:11-35`, `compose/page_counts.go:28-32`) have one impl and
  one test fake. `TopicPrepareIn` is a func returning a func
  (`unread/topic.go:22`, closure `compose/read_adapters.go:38-42`).
  `compose.NewTopicWriter` bypasses `unreadpg` unlike `NewFeedWriter`. ~50 lines.
- **D4. Long same-typed positional parameter lists.** VERIFIED (risk, not a bug).
  `unread.TopicMessages.ShownRanges` (`unread/topic.go:33`, 10 params: 3
  `kernel.ID`, 5 `int64`), `RunFloor` (`:36`), `TopicCounter.CountWithFirstUnread`
  (`unread/topics.go:17`), `conversation.Writer.InsertMessage`
  (`conversation/ports.go:71`), `realtime.NewCache` (`realtime/cache.go:94`,
  two adjacent durations). A swap compiles. Pass existing `Scope`/`TopicScope`
  structs or a params struct.
- **D5. Half-built `Reader` and test-only nil branches.** Wiring VERIFIED;
  panic SUSPECTED (latent). `cmd/ribbitto/main.go` builds
  `conversationpg.NewReader(pool, nil, nil, …)` for the sidebar and
  `cmd/ribbitto/live_sidebar.go:24` holds the concrete `*conversation.Reader`;
  calling `One`/`Many`/`Members`/`Pins` would nil-panic (`reader.go:36`).
  Narrow the interface `liveSidebar` takes. Nil-notifier branches exist only
  for tests (`posting.go:199`, `branch.go:61`, `pinning.go:97`, `reading.go:96`);
  use a no-op notifier in tests instead.
- **D6. `validateText` copied three times.** VERIFIED.
  `internal/identity/validation.go:69`, `internal/org/validation.go:67`,
  `internal/conversation/validation.go:12` (incl. the U+3000 rule). Move to
  `kernel` (or a small shared package allowed by the manifest). ~30 lines.
- **D7. Dead code.** VERIFIED with `deadcode`. `org.HandleChanger` chain
  (`org/handle.go:33-61`, `orgpg.go:46`, `org/internal/postgres/member.go:23-29`)
  is unreachable from all binaries ("awaiting its handler"). Delete it until
  the handler exists (the issue keeps the design).
- **D8. Module `doc.go` files narrate.** VERIFIED. `realtime/doc.go` (~55 of 68
  lines narrate), `conversation/doc.go` (~25/40), `unread/doc.go` (~28/36),
  `web/middleware/doc.go` (~20/28), `web/doc.go`. AGENTS.md says module roots
  list owned tables, event kinds and reasons, not APIs. `identity` and `org`
  are good models.
- Concurrency: no lock-ordering bug found. SUSPECTED minor: maps that never
  shrink (`realtime/hub.go:40` `hub.orgs`, `presence/presence.go:90`,
  `typing/state.go:116` empty summaries kept) — bounded by orgs/topics touched,
  low risk. `org` stream authorizer has its own LRU beside `realtime.Cache`
  (`stream_authorizer.go:41-44,160-170`) — do not rewrite without stream-cost
  measurements.

---

## 3. Web layer (`internal/web`, `web/src`)

- **W1. idiomorph loaded on every page, never used.** VERIFIED.
  `internal/web/view/layout.templ:31`; the only `hx-ext` is `sse`
  (`view/channel.templ:357`); no `morph` anywhere else. Remove the script tag
  and the vendored file, and its pins in `web/static/vendor_test.go:18`,
  `internal/web/handler_test.go:63,347`, `tools/typedscripts/main.go:106`,
  `tools/languagecheck/rules.go:94,98`, `tools/versioncheck/main.go:117`,
  `docs/dependency-updates.md`.
- **W2. Not-found mapping differs at ~14 sites.** Code VERIFIED; 500 outcome
  SUSPECTED. `channel.go:389` (post), `channel_image.go:58`, `branch.go:120`
  check only `ErrChannelNotFound`; `message_link.go:24` only
  `ErrMessageNotFound`; `channel.go:326` omits `ErrTopicNotFound`; `pin.go:54`,
  `quote.go:58`, `channel.go:485` check wider sets. Membership revoked
  mid-request may yield 500. Add one `isNotFound(err)` (or a shared wrapped
  sentinel). Reproduce first with a test.
- **W3. Cross-language string contracts not generated.** VERIFIED.
  SSE event names (Go `stream_renderer.go:125,147`, `sidebar.go:26`,
  `pins_source.go:114`, `typing_owner.go:34`, `presence.go:71`; templ
  `channel.templ:266,365`, `pins.templ:29`; TS `composer.ts:232`,
  `message-stream.ts:224`, `pins-sync.ts:70`, `sidebar-unread.ts:171`);
  stream `want` interests (`view/channel.templ:170-179` vs `stream.go:290`,
  which uses `"overview"` instead of `PanelOverview`); form field names
  (`composer.ts:6` vs `channel.go:412,438`). Extend `tools/domcontract`.
- **W4. `channelPages` mixes dependencies and per-request state.** VERIFIED.
  20 fields, ~28 methods in 12 files (`channel.go:53-76`); `topicID`, `panel`,
  `after`, `overviewImageErr` mutated on value-receiver copies (`channel.go:99`,
  `read.go:264`, `branch.go:132`, `members.go:477,482`);
  `channel_image.go:54-57` clones the request and flips it to GET to
  re-dispatch. Split into a deps struct and a request struct. Medium risk.
- **W5. 429 rejection repeated at 10 sites.** VERIFIED. `branch.go:38`,
  `channel.go:196,381`, `pin.go:43`, `read.go:28`, `send_key.go:22`,
  `sidebar.go:53`, `mention.go:27`, `channel_image.go:33`, `typing.go:20`.
  Hard-coded `Retry-After` restates `middleware/member_ratelimit.go`;
  `typing.go` omits it; `stream.go:185` writes a body; org-route 429s are
  blank while auth routes render a localized page (`handler.go:141`), so
  no-JS create/post/pin/branch get a blank page. Add `limiter.RetryAfter()`
  + one `reject(w, r, limiter)`. ~35 lines.
- **W6. Small handler duplication.** VERIFIED. `HX-Request == "true"` at 6
  sites; nil-service 404 at 7; optional single-ID form parse 4×
  (`channel.go:400-431`, `typing.go:105`).
- **W7. `view.ChannelPage` ~50 fields** (`view/channel.templ:37-103`) serves
  page, composer, pins, pin response and sidebar; fragments get partly filled
  structs (`pin.go:207`, `pins_source.go:348`). Introduce sub-structs gradually.
- **W8. i18n: three placeholder styles, no plurals.** VERIFIED. `fmt.Sprintf`
  (`mention.templ:15`), `{count}` + `strings.ReplaceAll` (`channel.templ:837`),
  `strings.NewReplacer` (`branch.go:90`). `signup.go` builds IDs at runtime
  (`"setup.error."+name`), invisible to ID checks.
- **W9. Client.** VERIFIED. ~20 hand-written htmx event casts in 12 files
  (`composer.ts:9`, `message-stream.ts:8`, `pins-sync.ts:42-103`,
  `read-visibility.ts:8-9`) — declare once in `globals.d.ts`
  (`DocumentEventMap`). `composer.ts` is one 618-line function with 27
  listeners. 130 of 298 generated contract exports are unused.
- **Minor.** VERIFIED. `handler.go:261` stylesheet error 500 without log; org
  paths by string concatenation instead of URL helpers (`shell.templ:23,45,84,112`,
  `mention.templ:30`, `channel.templ:363`, `send_key.go:38`, `handler.go:115`);
  ~20 `templ.SafeURL` on internal paths disable sanitizing for no gain;
  SUSPECTED redundant `resolvePlace` on `?quote=` (`channel.go:335`).
- The flat `internal/web` package is fine (31 prod files, ~3.2k lines); no
  restructuring needed.

---

## 4. Tests

Browser tests: `internal/web` has one `TestBrowser` (~48 scenarios, pool of 3
browsers, `message_stream_browser_test.go:28-120`), sharded by
`tools/testshard/main.go:41-50`; `cmd/ribbitto` has 10 `Test*Browser`
functions, each launching Chrome, unsharded.

- **Q1. Browser tests duplicate TS unit tests.** VERIFIED pairs:
  - `internal/web/mention_picker_browser_test.go:278-286` (21 literals × 150 ms
    wait) ↔ `web/test/mention-token.test.ts:38-78` (same literals).
  - `internal/web/composer_pending_browser_test.go` ↔ `web/test/composer-queue.test.ts`:
    `normalized-limit` (466-494) ↔ 23-29; `capacity-all-states` (1022) ↔ 119;
    `hold-delay` (264, patches `setTimeout`) ↔ 228, 389;
    `conflict-uuid-lock` (200) ↔ 112, 417.
  - `cmd/ribbitto/sidebar_unread_browser_test.go:240,278,451,457` (backoff) ↔
    `web/test/unread-counts.test.ts:178`.
  - `internal/web/drafts_save_test.go:964-1000` (storage cap) ↔
    `web/test/drafts-saving.test.ts:57,146`.
  Keep one wiring case per rule; ~1,300–1,700 lines removable.
- **Q2. Hidden browser scenario.** VERIFIED. ~1,180 of 1,252 lines of
  `internal/web/drafts_save_test.go` are `testSaveDraftsBrowser` (from line 74,
  28 `t.Run`). Rename to `*_browser_test.go`.
- **Q3. Browser harness duplicated and drifting.** VERIFIED. `boundedPage` in
  `cmd/ribbitto/browser_helpers_test.go:48` and
  `internal/web/short_height_browser_test.go:155` (the web copy lacks
  late-page close and `WithPanic`); three bounded-step implementations
  (`cmd` `inputStep` :99; `internal/web/browser_helpers_test.go:59-141`);
  two launchers with different flags. 47 of 54 browser files build their own
  `httptest` server. One shared support package is blocked by the rule
  "test code is not shared between packages"
  (`cmd/ribbitto/browser_helpers_test.go:45-46`). **NEEDS DECISION.**
- **Q4. Browser tests assert implementation details.** VERIFIED. Microtask
  order (`composer_pending_browser_test.go:259`), 101 assertions on
  `window.answers/requests/ordinaryRequests`, `draftWrites===1`
  (`drafts_save_test.go:1161`), 13 `.Load()` counters in
  `cmd/ribbitto/read_post_browser_test.go`, timer faking by matching hashed
  bundle names (`sidebar_unread_browser_test.go:33`), 10 files patch
  `setTimeout`/`Math.random`. `docs/architecture/web-tests.md` bans these only
  in unit tests.
- **Q5. Fixed waits.** VERIFIED. 20 in-page `setTimeout` waits
  (`mention_picker_browser_test.go:283,334,381`,
  `read_post_browser_test.go:124,184,228,299`, `topic_post_browser_test.go:107`,
  `message_actions_browser_test.go:184-199`, `message_time_browser_test.go:162`
  2 s) and `panel_sheet_browser_test.go:289,419`. Most prove "nothing
  happened", so on slow runners they pass when they should fail (SUSPECTED).
- **Q6. CI failures are dominated by test jobs** (520 failed-step entries);
  possibly flaky. Investigate before adding tooling (SUSPECTED).
- Authorization: no gap in 6 handlers checked (`pin_test.go:64`,
  `message_actions_page_test.go:47`, `mention_test.go:167-171`,
  `org_test.go:228`, `quote_test.go:119-128`, `presence_test.go:92`).
  SUSPECTED weakness: many inject `org.ErrNotFound` from a fake; real-DB proof
  of URL scoping is concentrated in `TestOrgRoutesAgainstPostgreSQL`.
- Fine as is: `markup_test.go`, `handler_test.go` (table-driven); 31 files use
  `synctest`.

---

## 5. Tooling, CI and workflow

Evidence: 4,797 CI runs, 489 failed; logs of 110 failed check runs analysed.

Commands that failed CI: golangci-lint 33 (staticcheck 29, errcheck 9, nilerr
6, depguard 2), docscheck 26 (≥22 are the size limit), tools' own tests 13,
go vet 7, languagecheck 3, formatcheck 3, apicheck 1. Never seen failing:
scopecheck, tablecheck, sourcecheck, contractliterals, typedscripts,
measurementskip, domcontract, deps (caveat: `check-tools` stops at the first
failure, so later tools could be masked).

- **T1. Keep:** lintfixtures, formatcheck, download, buildicons, tbls,
  webbuild, webtypes, apicheck, scopecheck/tablecheck (security invariant;
  note recurring cost of teaching them new SQL shapes: #685, #690, #730, #748,
  #754, #757, #762, #769, #1063).
- **T2. Remove** (**NEEDS DECISION**): `typedscripts` (244+355; legacy list
  empty since `622dfd0b`/#2130 — fold "no untyped .js" into languagecheck),
  `contractliterals` (273+126; `main.go:4-9` admits it is not a boundary),
  `measurementskip` (191+43; "not a security control",
  `allowlist.go:7`, `main.go:31`; argonreview remnant). ~1.2k lines.
- **T3. Freeze `sourcemaps`** (786+1,653, workflow + secret) until a first
  release; the repository has no tags. SUSPECTED premature.
- **T4. docscheck size limits → advisory**; keep link/anchor/path checks
  blocking. Largest source of tooling CI failures; 5 "bring under limit" PRs.
- **T5. Makefile.** VERIFIED. apicheck vet + race tests run twice
  (`Makefile:99-100` and `:106-107` via `./...`). `prepush` (`Makefile:120-127`)
  repeats languagecheck/apicheck/deps/docscheck/lint and `delivery.md:45`
  ("every push") contradicts the Makefile comment ("follow-up commit").
  Option: one `go -C tools run ./repocheck` driver for the nine
  repository-scanning checkers (one compile, one walk).
- **T6. Others:** `deps` is visibility only (rules enforced elsewhere;
  `docs/dependencies.md` touched in 84 commits); `unchanged` duplicates CI's
  `git status --porcelain`; `versioncheck` mostly duplicates Dependabot.
  Tool time is off CI's critical path (browser shards ~250 s set the 268 s median).
- **Workflow docs** (`docs/workflow/*`, 7 files, 1,203 lines, ~115 rules,
  138 PRs of churn): only 4 incident links (`reviewing.md:105,110`,
  `delivery.md:28`, `review-operations.md:76`). Same topics restated across
  files (review-round limits ×3, `ai-reviewed` ×5, quota ×4, nit rules ×5).
  Strongest cut candidates: third-AI review and post-cap triage, conversion
  parity rules (`reviewing.md:94-125`), long implementer brief
  (`delivery.md:5-35`). Argon still listed as an implementer
  (`docs/workflow/README.md:73-114`) though its launcher is gone.

---

## 6. Docs cleanup (decision 46)

Estimated ~6,600 of ~12,000 hand-written lines removable.

| # | Target | Action | Saved |
|---|---|---|---|
| 1 | `docs/measurements/*` (11 files) + `docs/message-paging-plans.md` | One `measurements/README.md`, ~10 lines per record (conclusion, conditions, SHA). Repair links in decisions 31, 32, 34 (allowed) and `internal/realtime/sidebar.go:45` | ~1,500 |
| 2 | `docs/architecture/web-*` (17 files) | Keep web-layers, web-tests rules, css-first criterion, browser-targets policy, source-map token rotation; delete approval history, "today's code" lists, check mechanics | ~1,080 |
| 3 | `realtime`, `replay`, `streaming`, `stream-scope`, `stream-limits`, `ephemeral-state`, `typing`, `pins` | Consolidate to cross-module contracts (sequence = commit order, audience authz, replay boundary/reset, vanished messages, why caps exist) | ~725 |
| 4 | `identity`, `request-flow`, `rate-limits`, `setup-and-signup`, `member-admission`, `csp` | Keep reverse-proxy contract, CSP rule, middleware-order constraints | ~475 |
| 5 | `cross-feature-access`, `import-checks`, `modules`, `features`, `packages`, `query-gate` | See §6.1; checks enforce most of it. `cross-feature-access.md` → ~8 rule lines into `modules.md`, delete file | ~435–530 |
| 6 | `load-client`, `load-restart`, `load-testing`, `stream-cost`, `dev-metrics` | Delete `load-client` (flag reference); merge rest into ~40 lines | ~400 |
| 7 | `unread-counts`, `unread-count-costs`, `unread-writes`, `unread-scope-writes` | Rules live in `docs/domain/unread*.md`; costs are measurements | ~390 |
| 8 | `ui-motion`, `ui-panels-and-motion`, `ui-components`, `ui-composer`, `ui-code`, `ui.md` | Merge motion into `ui-code.md` (covered 3×; `TestReducedMotion`, `TestArrivalMotion`); shrink rest | ~340 |
| 9 | `roadmap-mvp`, `roadmap-m9`, `roadmap-m10` | Delete; keep goals/outcomes/non-goals in `roadmap.md` | ~210 |
| 10 | `stream-authorization.md` | Keep rule (a2) + test names | ~160 |
| 11 | `database.md`, `recount-capacity.md` | Merge operator parts into `database.md`; repair decision 34 link | ~175 |
| 12 | `accessibility.md` + `accessibility-forms-and-targets.md` | Merge back (it was split); ✓ rules enforced by `TestPagesMarkup` | ~95 |
| 13 | `development.md`, `seed-data.md`, `database-tests.md` | Remove restated tool mechanics | ~160 |
| 14 | `docs/domain/*` | Light: `topics.md` "Tables", `pins.md` storage, issue tags | ~250 |
| 15 | `docs/workflow/*` | See §5 (needs maintainer decision) | ~250 |
| — | `vision`, `brand`, `ui-colours`, `database-locales`, `setup-and-backup`, `channel-name-upgrade`, `dependency-updates`, `*.txt` | Keep | 0 |

### Code comments

- 208 Go comment lines reference `#NN` (105 files; 161 in tests, 47 prod).
  ~49 pure history/attribution, ~9 should cite a decision instead, ~25
  load-bearing (19 in `feed_guard_budgets_test.go`, which
  `feed_guard_source_test` requires; `tools/languagecheck/rules.go:31`;
  `cost_test.go` provenance), ~125 a real "why" with a decorative `(#NNN)`.
- 27 comment lines name an AI reviewer (16 in `cmd/dev/main{,_test}.go`,
  e.g. `cmd/dev/main_test.go:248` "Codex on #1042"; `tools/unchanged/main.go:7,71,106`;
  `internal/web/read_visibility_browser_test.go:260,269`; `org_test.go:342`).
  All pure attribution — delete the attribution, keep the why.

---

## 7. Proposed convention changes

### 7.1 AGENTS.md "Layout and dependency direction" (replace 31 lines with ~15)

AGENTS.md is 144 lines against its own ~100-line cap. Draft:

```markdown
## Layout and dependency direction

`module_imports_test.go` (module manifest: paths, edges, owned tables),
depguard in `.golangci.yml` and `tools/tablecheck` enforce these; read
[`packages.md`](docs/architecture/packages.md) and
[`modules.md`](docs/architecture/modules.md) before changing a boundary,
and never relax a check to make an import pass.

- Import a module only at its root. Its store is imported only by its
  wiring (`<module>pg`); wiring only by `cmd/*`, `internal/compose` and
  tests; fixtures (`<module>test`) only by tests and higher fixtures, never
  production code.
- `internal/compose` (with sub-packages) is composition only, no business
  logic or service layer; only `cmd/*` and tests import it.
- Only `org` decides authorisation. Use cases return plain structs; only
  `internal/web` produces HTML.
- Only `internal/platform/postgres` imports `db/migrations`, and only stores
  import `pgxbridge`, tests included.
- New code goes in its module; read its `doc.go` first. Reach another
  feature only through its exported API; never query its tables. Writes
  spanning modules use the owners' injected factories in the flows the
  [feature map](docs/architecture/features.md) lists; a new flow needs its
  issue to say why.
```

### 7.2 Apply workflow principle 4 for real

Delete workflow rules with no incident link (or add the link if one exists).
Remove Argon from the implementer list if it is no longer used.

### 7.3 Testing policy (add to `docs/architecture/web-tests.md` or AGENTS.md Tests)

> Any rule that can be stated without a DOM (parsing, limits, backoff, queue
> states, storage caps, timer durations) is proved once, in `web/test/*.test.ts`
> or a Go unit test, one row per equivalence class. A browser scenario covers
> such a rule only through one wiring case; it never repeats the table's
> literals, reads timer values from a patched `setTimeout`, or counts
> requests, writes or microtasks. Browser scenarios assert what a user can
> observe: DOM state, focus, scroll, requests reaching the server and SSE
> delivery. Proving that something did not happen waits on an explicit
> signal, never a fixed delay. All browser files are named
> `*_browser_test.go` and share one harness; `cmd/ribbitto` keeps browser
> tests only for flows that need the real wiring and PostgreSQL. A PR adding
> a browser case names the user-visible behaviour no lower layer can prove.

### 7.4 Tool budget and sunset

A new tool or check needs an issue stating its size/time budget and the
incident it prevents. A tool that has caught nothing in N months (failing CI
log evidence) is a removal candidate at review. Lesson from argonreview.

### 7.5 Cheap enforcement (extend `tools/docscheck/comments.go`, `walkSourceComments`)

1. Hard rule: reject reviewer attribution in comments, e.g.
   `\b(Codex|Copilot|Grok|Argon|Claude|Gemini|Muse Code|[Tt]he maintainer|R\d+|K\d+)\b[^.]{0,20}\bon #\d+`
   and `accepted (on|in) #\d+`. ~44 current hits to fix in the same PR.
2. Optional diff ratchet: new comment lines matching `#\d{2,5}\b` fail
   (allow `TODO(#N)`, `issuecomment-` URLs, measurement provenance). Watch
   for CSS hex colours (`#000`, `#123456`) and ordinals.
3. Cap module-root `doc.go` at ~20 lines (formatcheck already checks doc.go).
4. Make docscheck size limits advisory (§5 T4).

### 7.6 Checks that could replace prose (from the import-rules audit)

- Manifest test: root `doc.go` names every table in its `ownsTables` (~15
  lines) → the ownership table in `modules.md` can go.
- AST test: unread's store references only `conversation.SequenceRange`.
- Every `internal/*` dir is a manifest root or in a fixed non-module list
  (kernel, platform, web, compose, lintfixture) → closes the memory-only gap.
- depguard rules for `cmd/loadgen`, `cmd/dev`, `cmd/capacity` (or delete the prose).
- `exactWiringStore` for every module.
- Collapse the 13 near-identical "no HTML outside web" depguard rules
  (`.golangci.yml:97-150`) into one; drop depguard's `composition` allow-list
  (`.golangci.yml:75-94`), which duplicates the manifest.

---

## 8. Keep as is

- Opaque `platform.Tx`/`Snapshot`, `pgxbridge` restricted to stores,
  `InTxDetachedCommit`/`ErrCommitUnknown`, raise only after confirmed commit
  (`internal/platform/postgres/tx.go:13-80`).
- Hub sequences as levels: CAS + close-and-replace channel, no lost wakeups
  (`internal/realtime/hub.go:120-135`, `hub_generations.go`).
- One `orgRoutes` table for registration and non-member 404 tests
  (`internal/web/org.go:26-86`).
- Stream lifecycle (`stream.go:176-243`, `stream_sender.go:31-67`) and security
  posture (nonce CSP, `allowEval:false`, no `templ.Raw`, sandboxed `/static/`).
- Do not merge unread into conversation; do not remove the `<module>pg` layer.
