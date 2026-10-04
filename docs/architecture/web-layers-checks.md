# Web layers: how the rules are checked

The rules are in [`web-layers.md`](web-layers.md).

## How the rules are checked

Checked by tools today: the layer imports (depguard), no `templ.Raw`
(forbidigo), and the markup rules in `TestPagesMarkup` ([a11y]).
The depguard `view` rule forbids non-test files in `internal/web/view`
from importing `internal/app`, `internal/identity`, `internal/org`,
`internal/conversation` or `internal/infra`, including sub-packages.
`TestPagesMarkup` also follows every `hx-get` and `hx-post` in each case,
in both languages, with `HX-Request: true`. It checks `hx-target` ids in
the requesting page, `hx-select` matches in the response, and every
`hx-select-oob` match in the response and its destination id in the page.
Selection may use response-only ids. The selector helper supports `#id`
and `#id > tag`, including comma-separated lists; new syntax needs explicit
test support. Non-id targets `this`, `closest …` and `find …` are allowed;
any ids they name are still checked in the page. The composer's
`hx-disabled-elt="find textarea, find button"` and `hx-sync="this:drop"`
are not swap selectors.

The data attributes in the DOM contract are asserted on the elements that
own them, where they are produced (#351): `TestMessagePagingHandler` on full
pages and Load older responses, `TestOrgRoutesAgainstPostgreSQL` on topic
pages (`data-topic`), and `TestMessageRendererSharesRenders` on the `message`
and `messages-moved` stream payloads.

`TestComponentsMarkup` renders `SignOutButton`, `MemberName`, `MessageItem`
and `LiveMessageItem` alone in both languages and applies the fragment
markup rules.
`TestPagesMarkup` also checks the composer fragments returned by handlers.

`TestPagesMarkup` also checks every `hx-get`/`hx-post` element for a plain
link or form with the same URL and HTTP method (`fallback_test.go`). URL
comparison is structural; paging success uses positive cursors in
`TestMessagePagingHandler`.

`TestMessagePostHandler` covers ordinary/HX success (303/200) and validation
errors (422). `TestMessagePagingHandler` covers Load older with and without
HX (200 full pages). `TestChannelHandlers` verifies that HX changes nothing
for unenhanced channel creation (303/422). Plain posting errors and history
responses assert the full layout; enhanced posting responses assert the
composer alone, without another history read.

Left to review, because a pattern check cannot prove them: that a stored
value is only a UX setting, that a script owns no application state, and
that a component makes no authorisation decision.

### Executed stream tests

`TestMessageStreamBrowser` in `internal/web/message_stream_browser_test.go`
runs the embedded `message-stream-v6.js`, vendored htmx and SSE extension
in headless Chrome through go-rod. An HTTP test server renders real
`ChannelScreen` and `MovedMessageItems` components without a database. Controlled
SSE deliveries and history responses cover both topic sides, bounded
insertion, duplicate replay, moves before/during Load older, stale/fresh
swaps, and abort/400 followed by another request. Exact ordered ID lists
detect duplicates and gaps; waits use DOM/lifecycle conditions, not sleeps.

Run `go test ./internal/web -run TestMessageStreamBrowser -v` with Chrome
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
- `fetch`, `XMLHttpRequest` and `indexedDB` identifiers.
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
globals by hand. `localStorage` and `sessionStorage` pass mechanically; the
check cannot prove that a stored value is only a non-sensitive UX setting.

## Audit, 2026-10-01 (M3)

Every HTML handler in `internal/web`, every templ component and every
application script in `web/static` was read against these rules, first on
2026-09-30 and again after M3 (#163).

| Where | Finding | Fixed by |
|---|---|---|
| `view/channel.templ`: `ChannelPage.Messages` | The handler now converts app entries to `view.Message` values | #194; depguard `view` rule |
| Composer script's `htmx:beforeSwap` made 422 responses swap | JavaScript decided what htmx swaps | #198: layout's htmx config |
| `message-stream-v6.js` swaps through `htmx.swap`: `outerHTML` for a message id already present, else append | The extension's swap style is fixed per element, so it cannot replace by id or append | Kept, by design: SSE glue; the HTML stays server-rendered (#159) |

Everything else conforms:

- **Handlers.** Pages, the composer fragment and the event stream render
  view models through templ.
- **Templates.** No I/O or authorisation; the announcement is escaped.
- **Core flows.** Plain forms and links work without JavaScript.
- **Scripts.** Focus, scroll, keyboard, local time and SSE glue; no requests,
  storage or globals.

[a11y]: ../accessibility.md
