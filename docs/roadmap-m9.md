# M9: Front-end foundation and UX

Goal and plan for milestone M9 (#1404) in the [roadmap](roadmap.md#after-the-mvp):
a front end that stays safe to change as it grows, and faster switching.

Toolchain choices (#1356) and client-side UX rules (#1361) are pending
decisions; M9 records their planned order and outcomes without deciding them
in advance.

## Outcomes

1. **Typed, unit-tested client code.** Client code is typed and its logic
   unit-tested; no untyped script can be added.
2. **Shared DOM contract and hashed delivery.** Scripts and templates share
   one DOM contract generated from Go; scripts are delivered under
   content-hashed names.
3. **Written conventions and browser targets.** Front-end conventions and
   target browsers are written down.
4. **Opening a conversation stays cheap.** The cost of opening a
   conversation stays bounded as messages and features grow: a smaller first
   render (#1477, with shown-interval read state, #1486, and permalinks to
   recent messages that still land on a page showing them), the phone
   first-paint fix (#1480), smaller per-message markup (#1466's F-series with
   #1455 and #1457) and a standing per-message budget check in CI (#1478).
5. **Unread indicators on the tab.** Unread counts reach the tab title and
   favicon.

## Position in the roadmap

Confirmed by the maintainer (2026-10-19): after M7 (the composer track),
M9's foundation (phases 1–5) starts first and runs alongside the *Basics*
track (private channels → direct messages → invitations). *Basics* does not
wait for M9 as a whole, and M9's UX items (phase 6) do not block *Basics*.
Where both change the same code, the two issues state their dependency and
order before either is made `ready`; in particular, private channels and the
conversation cache share one access model (pending decision #1361), and
whichever lands second adds the tests for the other.

## Order

Phases may overlap where nothing depends on it:

1. **Documents, any time:** #1378 CSS-first criterion · #1406 target
   browsers · #1405 roadmap entry.
2. **Measure first:** #1407 toolchain spike (build versus no build) · #1408
   network-delay profile. Both use #1149's harness; ask for one approval and
   cap ([investigations](workflow/investigations.md)) and run them in one
   session to save tokens.
3. **Decisions (pending):** #1356 first (toolchain; to amend decisions
   [2](decisions/02-server-rendered-html-with-htmx-not-an-spa.md),
   [19](decisions/19-web-layers-server-owned-html-htmx-swaps-javascript-as-enhancement.md),
   [35](decisions/35-tooling-in-go.md) and
   [40](decisions/40-plain-javascript-by-default-library-islands-by-measured-exception.md)),
   then #1361 (client-side UX; to amend decisions 19,
   [30](decisions/30-one-organisation-wide-stream-per-tab-filtered-by-its-interests.md),
   [37](decisions/37-composer-javascript-pending-posts-keys-and-drafts.md)
   (c) and
   [41](decisions/41-client-storage-indexeddb-by-data-class.md), and record
   the cache's accepted safety model), so the two never edit decision 19 at
   once. Then #1409 front-end conventions.
4. **Toolchain:** #1410 type check · #1411 hashed script delivery · #1412
   unit tests · #1413 DOM contract · #1414 keep new code typed · #1415 Node
   in the AI workflow. The Svelte-path issues (#1172, #1194, #1187, #1166)
   are closed as superseded by #1356's decision pull request, not re-scoped,
   because decision 40 cites them by number.
5. **Conversions,** stateful first: #1416 composer queue state machine →
   #1417 render from queue state · #1418 drafts and unread counts · #1419
   stream scripts · #1420 composer helpers · #1421 remaining small scripts.
6. **Client-side UX:** #1422 tab title and favicon. #1424 (conversation
   swap) and #1425 (in-memory cache) are held: #1467 kept full-page
   navigation (server-rendered HTML with htmx), and #1361's item A1 is
   deferred. #1408 is the only route to reconsider partial navigation, and
   only through a new design, a new measurement and the maintainer's
   explicit approval, never the held design restored as is.
7. **Review:** #1426 whole-milestone review, measurement and browser
   verification.

## Acceptance criteria

#1426 checks M9 as a whole against the criteria in #1404, each with a test,
a measurement or a recorded test step. In short: switching (rapid switches,
back and forward) ends on the right conversation and stream; a lost or
interrupted navigation leaves no second stream; a disconnect and recovery
loses and duplicates nothing; unsent text and pending or failed own posts
survive a switch, a reconnect and recovery; reading, posting and switching
work with JavaScript off; opening a conversation at 50 and 200 messages
stays within the limits stated before measuring, and the per-message budget
check (#1478) passes; nothing regresses beyond the stated limits. The cache
criteria (fail-closed, discard on loss of access) apply only if #1361's A1
is revisited.

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
