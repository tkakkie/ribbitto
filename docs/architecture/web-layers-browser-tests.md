# Web layers: browser tests and audit

Shared browser tests and the M3 audit for [web-layer checks](web-layers-checks.md).

### Shared browser tests

`registerBrowserScenarios` in `internal/web/message_stream_browser_test.go`
registers `TestBrowser` scenarios.
`RIBBITTO_BROWSER_SHARD=k/n` runs one local shard; if shell-exported,
`make check` silently runs a subset.
Stream cases run the embedded `message-stream-v14.js`, vendored htmx and SSE extension
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

## Audit, 2026-10-01 (M3)

Every `internal/web` HTML handler, templ component and `web/static` application
script was read against these rules on 2026-09-30 and after M3 (#163).

| Where | Finding | Fixed by |
|---|---|---|
| `view/channel.templ`: `ChannelPage.Messages` | The handler now converts app entries to `view.Message` values | #194; depguard `view` rule |
| Composer script's `htmx:beforeSwap` made 422 responses swap | JavaScript decided what htmx swaps | #198: layout's htmx config |
| `message-stream-v14.js` swaps through `htmx.swap`: `outerHTML` for a message id already present, else append | The extension's swap style is fixed per element, so it cannot replace by id or append | Kept, by design: SSE glue; the HTML stays server-rendered (#159) |

Everything else conforms:

- **Handlers.** Pages, the composer fragment and the event stream render
  view models through templ.
- **Templates.** No I/O or authorisation; the announcement is escaped.
- **Core flows.** Plain forms and links work without JavaScript.
- **Scripts.** Focus, scroll, keyboard, local time and SSE glue; no requests,
  storage or globals.
