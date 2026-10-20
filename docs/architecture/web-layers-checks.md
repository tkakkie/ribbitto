# Web layers: how the rules are checked

The rules are in [`web-layers.md`](web-layers.md),
[`web-contracts.md`](web-contracts.md) and
[`web-javascript.md`](web-javascript.md).

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

The data attributes in the [DOM contract](web-contracts.md) are asserted on the elements that
own them, where they are produced (#351): `TestMessagePagingHandler` on full
pages and Load older responses, `TestOrgRoutesAgainstPostgreSQL` on topic
pages (`data-topic`), `TestMessageRendererSharesRenders` on the `message`
and `messages-moved` stream payloads, and `TestPagesCarryOwnSession` on the
session marker of both organisation pages.

`TestComponentsMarkup` renders `SignOutButton`, `MemberName`, `MessageItem`
and `LiveMessageItem` alone in both languages and applies the fragment
markup rules.
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

**Current exceptions:** review the no-application-state
rule with [decision 34](../decisions/34-live-unread-counts.md)'s narrow allowance
for one script's sidebar badge numbers and bounded recovery state. It never
creates elements or makes requests. Review composer code with
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
cap ties, trim/scope, overlap recovery, expiry,
errors/swaps. Markers order reads.
Restore: navigation, IME/pagehide, 422, ended sessions, no input/typing/focus,
own-key guards, future edits/deletions. Held sends: outcomes, edits, queues, busy drops,
forced 409 swaps; rotated-key Retry/Remove after Edit/resend, success/failure;
in-place 409 recovery; dirty cleanup keeps edit time/debounce against tab writes.
Retry: saved/unsaved/no newer text, both save/response orders; Edit/Remove:
queued success, tombstones, backspacing, newer text.

### Shared browser tests

`TestBrowser` scenarios are registered in `registerBrowserScenarios` in
`internal/web/message_stream_browser_test.go`.
`RIBBITTO_BROWSER_SHARD=k/n` runs one local shard; if shell-exported,
`make check` silently runs a subset.
Stream cases run the embedded `message-stream-v13.js`, vendored htmx and SSE extension
in headless Chrome via go-rod, with database-free `ChannelScreen` and
`MovedMessageItems` test servers. Controlled SSE and history responses cover both topic sides, bounded
insertion, duplicate replay, moves before/during Load older, stale/fresh
swaps, removal of the moved first-unread message's divider, preservation on
other moves, and abort/400 then another request. IDs detect duplicates/gaps; waits use DOM/lifecycle gates.
Panel-sheet checks use `noReadWindow` to observe that no POST occurs.
Script-disabled tabs use `navigateLoaded` for readiness; rod’s `WaitLoad`
needs page JavaScript and can hang (#1129).

Composer cases run real signal/post handlers with request and DOM gates: IME,
input throttling, draft isolation, independent in-flight signals, 422/retry,
replacement, closed/fresh generations, a post held in flight while typing
and composing (#1173), enhanced latest feed/topic posts and
plain feed/topic posts (latest/older). `TestTopicPostBrowser` (`cmd/ribbitto`)
posts from a topic view through real PostgreSQL and the real stream.
Feed/topic cases cover both orders, replay, feed moves,
same-text collisions, JavaScript whitespace, failures and Retry/Edit/Remove.
Topic orders: post/move/answer, answer/move, lost/move/retry,
lost/retry-to-B/post/move (Retry before any stream), move-only/answer.
Checks cover same-task removal, next sends, frame identities and desktop/phone
positions; PostgreSQL covers cursors and lost-response Retry via interception.
The throttle timer is gated.
Typing indicator cases use production handlers, stream lifecycle, owner and writer
for A/feed/B scope, self-exclusion, English/Japanese counts, expiry, send, multiple
streams/last close and current/empty reconnects. Transport cases assert native
Last-Event-ID and the page cursor after populated and empty typing frames.

The move form case runs the real branch handler with and without scripts:
hidden and skipped by Tab with nothing checked, shown on Space with focus kept
and the list's size unchanged, its errors, and a submitted move.

`go test ./internal/web -run TestBrowser -v` uses installed Chrome, never downloading it.
Missing or unlaunchable browsers skip locally; CI sets `RIBBITTO_REQUIRE_BROWSER=1` to fail.
The Go replay model retains only in-flight moves, modeling append,
replacement and bounded insertion without sorting.

### JavaScript pattern check

`TestApplicationJavaScript` in `web/static/javascript_test.go` checks every
direct `web/static/*.js` file, regardless of its name or version; it does
not descend into subdirectories such as `vendor/`. Names must end in
`-vN.js`, where N is one or more decimal digits.

The check matches source patterns on each line after masking `//` and
`/* ... */` comments and single-, double- and backtick-quoted contents
(including escapes). Quote delimiters remain visible for string timers.
It rejects:

- `innerHTML`/`outerHTML` assignments, including compound assignments such
  as `+=`; calls to `insertAdjacentHTML`, `document.write`,
  `document.writeln` and `createContextualFragment`. Reads and comparisons
  of HTML properties pass.
- `fetch`, `XMLHttpRequest`, `sendBeacon`, `WebSocket`, `EventSource`, `indexedDB` identifiers; `htmx.ajax` outside `pins-sync-v2.js`; `localStorage` outside `message-drafts-vN.js`.
- Calls to `eval`, `new Function`, and `setTimeout`/`setInterval` with a
  directly quoted first argument. These evaluation restrictions restate
  the existing CSP without `'unsafe-eval'` ([`rendering.md`](rendering.md)).
- `var`/`let`/`const` and named `function` declarations at zero brace and
  parenthesis depth (a keyword after `.`, such as `options.var`, is a
  property name and passes), plus direct dot-property assignments and increments
  or decrements on `window`/`globalThis`. No particular file structure is
  required; multiple IIFEs and scoped event handlers pass.

This is a bounded pattern check, not a JavaScript parser. It does not
resolve aliases, computed properties, expressions split across lines or
template interpolations. Regular-expression literals are not recognized;
their contents can be mistaken for code, comments or delimiters. Delimiter
depth is not scope analysis: block-level `var`, implicit globals and other
indirect writes can escape it, while some named function expressions can
look like declarations. Reviewers check these cases, all HTML writes and
globals by hand. Stored values need the review described above.

## Audit, 2026-10-01 (M3)

Every HTML handler in `internal/web`, every templ component and every
application script in `web/static` was read against these rules, first on
2026-09-30 and again after M3 (#163).

| Where | Finding | Fixed by |
|---|---|---|
| `view/channel.templ`: `ChannelPage.Messages` | The handler now converts app entries to `view.Message` values | #194; depguard `view` rule |
| Composer script's `htmx:beforeSwap` made 422 responses swap | JavaScript decided what htmx swaps | #198: layout's htmx config |
| `message-stream-v13.js` swaps through `htmx.swap`: `outerHTML` for a message id already present, else append | The extension's swap style is fixed per element, so it cannot replace by id or append | Kept, by design: SSE glue; the HTML stays server-rendered (#159) |

Everything else conforms:

- **Handlers.** Pages, the composer fragment and the event stream render
  view models through templ.
- **Templates.** No I/O or authorisation; the announcement is escaped.
- **Core flows.** Plain forms and links work without JavaScript.
- **Scripts.** Focus, scroll, keyboard, local time and SSE glue; no requests,
  storage or globals.

[a11y]: ../accessibility.md
