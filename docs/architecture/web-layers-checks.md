# Web layers: how the rules are checked

Rules: [layers](web-layers.md), [DOM][c],
[JS](web-javascript.md).
[Decision 43](../decisions/43-typescript-with-a-go-driven-build.md).
Browser tests and audit: [`web-layers-browser-tests.md`](web-layers-browser-tests.md).

## How the rules are checked

Tools check layer imports (depguard), no `templ.Raw` (forbidigo),
and markup in `TestPagesMarkup` ([a11y]).
Depguard forbids non-test `internal/web/view` imports of `internal/identity`,
`internal/org` or `internal/conversation`, including sub-packages.
`TestPagesMarkup` follows each case's `hx-get`/`hx-post` in both languages
with `HX-Request: true`: `hx-target` ids in the requesting page, `hx-select`
response matches, every `hx-select-oob` response match and page destination id.
Selection may use response-only ids. Selectors support `#id`, `#id > tag` and comma-separated lists;
new syntax needs test support. Non-id targets `this`, `closest …` and `find …` are allowed;
any ids they name are still checked in the page. The composer's
`hx-disabled-elt='find button[type="submit"]:not([form])'` and `hx-sync="this:drop"`
are not swap selectors. The sibling typing form is checked for its POST
route, action/method, generation-only controls, no-swap and empty 204;
other requests retain the existing response and fallback checks.

Tests assert [DOM contract][c] attributes on their owning elements (#351):
`TestMessagePagingHandler` on full pages and Load older responses, `TestOrgRoutesAgainstPostgreSQL` on topic
pages (`data-topic`), `TestMessageRendererSharesRenders` on the `message`
and `messages-moved` stream payloads, and `TestPagesCarryOwnSession` on the
session marker of both organisation pages.

`TestComponentsMarkup` applies fragment markup rules to `SignOutButton`,
`MemberName`, `MessageItem` and `LiveMessageItem` alone in both languages.
`TestPagesMarkup` also checks the composer fragments returned by handlers.

`TestPagesMarkup` checks plain fallbacks (`fallback_test.go`): matching URL/method,
except the read-only quote form's conversation GET and channel chip POST (#1160).
URLs compare structurally; `TestMessagePagingHandler` checks paging success.

`TestMessagePostHandler` covers ordinary/HX success (303/200) and validation
errors (422) for feeds and topics; an HX topic fragment targets the posted
message's topic. `TestTopicPostScopeAgainstPostgreSQL` keeps unknown,
other-channel and non-member topic posts 404 with and without HX. `TestMessagePagingHandler` covers Load older with and without
HX (200 full pages). `TestChannelHandlers` verifies that HX changes nothing
for unenhanced channel creation (303/422). Plain posting errors and history
responses assert the full layout; enhanced posting responses assert the
composer alone, without another history read.

Reviewers check what patterns cannot prove: stored values stay within UX settings,
decision 37's drafts or [decision 41](../decisions/41-client-storage-indexeddb-by-data-class.md)'s
classes (1 public, 2 organisation, 3 message content, requiring its own
decision); a script owns no application state beyond decisions 34 and 37;
composer request URLs come from server-rendered attributes; components make
no authorisation decisions.

**Current exceptions:** [decision 34](../decisions/34-live-unread-counts.md)
allows one script's sidebar badge numbers and bounded recovery state, never
creating elements or making requests. Review composer code with
[decision 37](../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md)'s allowances: pending own posts, the idempotency key and
session-bound drafts, with their bounds and owner checks.

### Decision 37 checks

Checks: pending bodies' `textContent`, requests, markup
(`TestComponentsMarkup`). Application scripts forbid `indexedDB` until its first
allowed use scopes the check to its file, like `localStorage` for
`message-drafts-vN.js`. Decision 41 changes no checks.
`TestBrowser/drafts`: sign-in cleanup (#1178), millisecond boundaries,
malformed keys, cross-tab notices, blocked scripts, failed sign-out, signed-in
errors. `TestBrowser/drafts-org`: both organisation pages (#1181), stale sessions
keep newer/same-millisecond entries unread; later sessions/members remove earlier
ones; malformed values/expired own sessions removed. `TestDraftComposerPlace`:
latest/history attributes.
`TestBrowser/drafts-save`: debounce, edit times, lifecycle/IME, tombstones, bounds,
cap ties, trim/scope, overlap recovery, expiry, errors/swaps. Markers order reads.
`expiry-clock/{save,edit,restore,timer,markers}`: device-only expiry removes both
own places, preserves foreign sessions/members, text and sending dispatch;
`normal`, `monotonic`, `boundary`: saving/restore/markers, monotonic-only expiry,
expiry-1/exact expiry. Scripted timers.
Restore: navigation, IME/pagehide, 422, ended sessions, no input/typing/focus,
own-key guards, future edits/deletions. Held sends: outcomes, edits, queues, busy drops,
forced 409 swaps; rotated-key Retry/Remove after Edit/resend, success/failure;
in-place 409 recovery; dirty cleanup keeps edit time/debounce against tab writes.
Retry: saved/unsaved/no newer text, both save/response orders; Edit/Remove:
queued success, tombstones, backspacing, newer text.

See [shared browser tests](web-layers-browser-tests.md#shared-browser-tests) and the [M3 audit](web-layers-browser-tests.md#audit-2026-10-01-m3).

### JavaScript pattern check

`TestApplicationJavaScript` in `web/static/javascript_test.go` checks direct
`web/static/*.js` and `web/src/*.ts` (including `.d.ts`), never subdirectories.
Both run; absent JavaScript fails until #1831. JavaScript names require
`-vN.js` (decimal N); TypeScript names do not.

Comments and quoted contents (with escapes) are masked before per-line checks;
quote delimiters remain for string timers. It rejects:

- `innerHTML`/`outerHTML` assignments, including compound assignments such
  as `+=`; calls to `insertAdjacentHTML`, `document.write`,
  `document.writeln` and `createContextualFragment`. Reads and comparisons
  of HTML properties pass.
- `fetch`, `XMLHttpRequest`, `sendBeacon`, `WebSocket`, `EventSource`, `indexedDB` identifiers; `htmx.ajax` outside `web/src/pins-sync.ts`; `localStorage` outside `message-drafts-vN.js`.
- Calls to `eval`, `new Function`, and `setTimeout`/`setInterval` with a
  directly quoted first argument. They restate
  CSP without `'unsafe-eval'` ([`csp.md`](csp.md)).
- JavaScript declarations at zero brace/parenthesis depth (`var`/`let`/`const`,
  named `function`; properties like `options.var` pass). TypeScript permits
  module declarations. Both reject dot-property writes and
  increments/decrements on `window`/`globalThis`; TypeScript also rejects casts
  of either global. IIFEs and scoped handlers pass. TypeScript has no
  `localStorage` allowance (#1418 adds the drafts entry).

This pattern check is no parser or security boundary: it cannot resolve aliases,
computed properties, multiline expressions or template interpolations. It may
mistake regular-expression literals for code, comments or delimiters. Delimiter
depth cannot determine scope: block-level `var`, implicit globals and indirect
writes may escape; named function expressions may look like declarations.
Reviewers check these cases, all HTML writes and globals by hand, and review
stored values as above.

[a11y]: ../accessibility.md
[c]: web-contracts.md
