# Web layers: how the rules are checked

The rules are in [`web-layers.md`](web-layers.md),
[`web-contracts.md`](web-contracts.md) and
[`web-javascript.md`](web-javascript.md).

## How the rules are checked

Checked by tools today: the layer imports (depguard), no `templ.Raw`
(forbidigo), and the markup rules in `TestPagesMarkup` ([a11y]).
The depguard `view` rule forbids non-test files in `internal/web/view`
from importing `internal/identity`, `internal/org` or
`internal/conversation`, including sub-packages.
`TestPagesMarkup` also follows every `hx-get` and `hx-post` in each case,
in both languages, with `HX-Request: true`. It checks `hx-target` ids in
the requesting page, `hx-select` matches in the response, and every
`hx-select-oob` match in the response and its destination id in the page.
Selection may use response-only ids. The selector helper supports `#id`, `#id > tag` and comma-separated lists;
new syntax needs test support. Non-id targets `this`, `closest …` and `find …` are allowed;
any ids they name are still checked in the page. The composer's
`hx-disabled-elt="find button"` and `hx-sync="this:drop"`
are not swap selectors. The real sibling typing form is checked for its POST
route, matching action/method, generation-only controls, no-swap and empty 204;
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

`TestPagesMarkup` also checks every `hx-get`/`hx-post` element for a plain
link or form with the same URL and HTTP method (`fallback_test.go`). URL
comparison is structural; paging success uses positive cursors in
`TestMessagePagingHandler`.

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

Checks cover HTML generation (`textContent` for pending bodies), requests
and standalone markup (`TestComponentsMarkup`). The
`indexedDB` pattern stays in force in every application script until the
first allowed use; its issue makes the check file-scoped, as `localStorage`
is limited to `message-drafts-vN.js`. No checks change for decision 41.
`TestBrowser/drafts` checks sign-in cleanup (#1178): millisecond boundaries,
malformed keys, cross-tab notices, blocked scripts, failed sign-out and
signed-in errors. `TestBrowser/drafts-org` checks both organisation pages
(#1181): stale sessions keep newer/same-millisecond entries unread; later
sessions or members remove earlier ones; malformed values and expired own
sessions are removed. Pending markup, replacement and recovery are checked
below. Planned: #1162 tests a controlled interleaving: a tab of an earlier session writes
  after a later session's page has loaded and cleaned, and the later session
  never restores that text. It also tests sign-out with two tabs open and a
  debounced save pending.

### Shared browser tests

`TestBrowser` in `internal/web/message_stream_browser_test.go` is the shared
browser suite. Its stream cases run the embedded `message-stream-v12.js`, vendored htmx and SSE extension
in headless Chrome through go-rod. A test server renders
`ChannelScreen` and `MovedMessageItems` components without a database. Controlled
SSE deliveries and history responses cover both topic sides, bounded
insertion, duplicate replay, moves before/during Load older, stale/fresh
swaps, removal of the moved first-unread message's divider, preservation on
other moves, and abort/400 followed by another request. Ordered IDs detect duplicates and gaps; waits use DOM/lifecycle gates.
The panel-sheet reading checks use a named observation window,
`noReadWindow`, when no POST is expected.
Script-disabled tabs wait for document readiness without page-side event
listeners; `navigateLoaded` provides this for the message-time and
panel-sheet tests. rod's `WaitLoad` listens in page JavaScript, so it hangs
there if the load is still pending (#1129).

Composer cases run real signal/post handlers with request and DOM gates: IME,
input throttling, draft isolation, independent in-flight signals, 422/retry,
replacement, closed/fresh generations, a post held in flight while typing
and composing (#1173), enhanced latest feed/topic posts and
plain feed/topic posts (latest/older). `TestTopicPostBrowser` (`cmd/ribbitto`)
posts from a topic view through real PostgreSQL and the real stream.
Feed pending cases cover both response/delivery orders, reconnect replay,
feed moves, same-text collisions, JavaScript whitespace, failure classes and
Retry/Remove. Per-frame identity counts and desktop/phone body positions
check replacement; PostgreSQL checks pending/response/delivery cursor phases
and a committed send with its response lost. The composer harness controls only the two-second throttle timer, without sleeps.
Typing indicator cases use production handlers, stream lifecycle, owner and writer
for A/feed/B scope, self-exclusion, English/Japanese counts, expiry, send, multiple
streams/last close and current/empty reconnects. Transport cases assert native
Last-Event-ID and the page cursor after populated and empty typing frames.

The move form case runs the real branch handler with and without scripts:
hidden and skipped by Tab with nothing checked, shown on Space with focus kept
and the list's size unchanged, its errors, and a submitted move.

Run `go test ./internal/web -run TestBrowser -v` with Chrome
installed. No browser is downloaded. Missing or unlaunchable browsers skip
locally; `RIBBITTO_REQUIRE_BROWSER=1` makes either fatal and is set in CI.
The Go replay model retains only in-flight moves and models append,
replacement and bounded insertion without sorting the list.

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
- `fetch`, `XMLHttpRequest`, `sendBeacon`, `WebSocket`, `EventSource` and
  `indexedDB` identifiers, and `htmx.ajax`; `localStorage` outside
  `message-drafts-vN.js`.
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
| `message-stream-v12.js` swaps through `htmx.swap`: `outerHTML` for a message id already present, else append | The extension's swap style is fixed per element, so it cannot replace by id or append | Kept, by design: SSE glue; the HTML stays server-rendered (#159) |

Everything else conforms:

- **Handlers.** Pages, the composer fragment and the event stream render
  view models through templ.
- **Templates.** No I/O or authorisation; the announcement is escaped.
- **Core flows.** Plain forms and links work without JavaScript.
- **Scripts.** Focus, scroll, keyboard, local time and SSE glue; no requests,
  storage or globals.

[a11y]: ../accessibility.md
