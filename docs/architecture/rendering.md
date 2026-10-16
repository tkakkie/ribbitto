# Rendering and assets

templ components live in `internal/web/view`. Static assets are embedded
from `web/static` into the binary; the stylesheet URL carries the SHA-256
of the embedded CSS, so assets can be cached as immutable. In development
(`make dev`, `RIBBITTO_DEV_ASSETS=web/static`) assets are read from disk and
the hash is recomputed per request, so rebuilt CSS appears without a
restart.

Fluent icons use a versioned, same-origin SVG sprite in `web/static/vendor`
through the shared `Icon` templ component ([UI convention](../ui-code.md#icons)).
The sprite is embedded and served like the other assets; its external `<use>`
references require no change to the Content Security Policy below.

Channel illustrations ship from the embedded `channel-images-v1` directory
with versioned URLs. Handlers resolve stored keys or choose a stable
ID-based fallback for NULL or unknown keys, then pass the URL to the shared
view component. It reserves 24 px in sidebar rows and 32 px in the header,
uses empty alt text and asynchronous decoding, and lazy-loads sidebar images.

## Content Security Policy

HTML routes are registered on the `routes` mux
in `internal/web.NewHandler`, behind `middleware.SecurityHeaders`. Each
response gets a fresh 32-byte `crypto/rand` nonce, base64-encoded and passed
to templ through `templ.WithNonce`; every script in the shared layout uses
`templ.GetNonce(ctx)`. The policy is defined in one place in
`internal/web/middleware/security.go`:

```text
default-src 'self'; script-src 'nonce-<nonce>'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'
```

There is no `'self'` in `script-src`: even same-origin external scripts
require the response's nonce. This is defence in depth against HTML
injection. HTML responses also send `X-Content-Type-Options: nosniff` and
`Referrer-Policy: same-origin`; `/static/` and `/healthz` bypass this
middleware. The static handler sends its own `X-Content-Type-Options: nosniff`
and `Content-Security-Policy: default-src 'none'; sandbox` on every response,
found or not, including the explicit 405 (`Allow: GET, HEAD`) for any other
method (#937): an SVG opened directly is a document on this origin, so
it runs nothing and loads nothing. Pages are unaffected, because a file used
as `<img>`, `<use>`, a script or a stylesheet does not apply its own policy;
`TestBrowser/static-headers` checks that icons and illustrations
still paint. Caddy owns `Strict-Transport-Security` in deployment.

The layout's `htmx-config` meta tag sets `allowEval: false`,
`allowScriptTags: false` and `includeIndicatorStyles: false`. htmx cannot
evaluate attribute scripts, execute scripts from swapped fragments, or
inject its default inline indicator stylesheet. Do not use inline scripts,
`hx-on`, or other attribute scripts; keep JavaScript and styles in external
assets.

The same meta tag sets `responseHandling` to htmx's defaults (`204`: no
swap; `[23]..`: swap; `[45]..`: no swap, error), with
409 and 422 entries with `swap: true, error: false` before `[45]..`. The first
matching entry wins: composer errors replace `#message-composer-state`, branch
errors replace `#branch-feedback`. New htmx requests inherit both rules
([`web-layers.md`](web-layers.md)). Other status codes retain their defaults.

Message timestamps use `message-time-v2.js`, loaded with the response nonce
on every channel page, empty or not, because htmx does not run scripts in
swapped fragments. The server renders `<time datetime>` in UTC, truncating
only the attribute to millisecond precision; stored timestamps and the
visible UTC fallback text are unchanged. The script localises the initial
page once, then only the element reported by each `htmx:load` and its
descendants. It reuses one `Intl.DateTimeFormat` for the page language and
tracks localised elements in a `WeakSet` so overlapping load events do not
rewrite them. Invalid dates retain their fallback, as do all timestamps
when JavaScript is disabled. From `desk` the element reserves a width that
fits both texts, so the rewrite moves nothing beside it (#1094,
[`ui-components.md`](../ui-components.md#shared-components)).
Application script URLs are versioned because static assets are immutable.

`message-composer-v9.js` also loads with the response nonce on channel pages.
One key-to-command function maps Enter to `send` and Shift+Enter to `newline`,
ignoring IME composition (`isComposing` or keyCode 229); one submit function
uses the native form submission path through `requestSubmit`. The latest-page form uses
htmx to replace only `#message-composer-state` (#1173). In flight only the send
button is disabled, `hx-sync="this:drop"` drops another send, and the `sending:`
variant (htmx's `htmx-request` class) shows a polite "Sending…" status over the
help, which keeps its space; the textarea stays usable. A send times out after
30 s (`hx-request`). Failures fill `#message-send-alert` (#1179,
[web JavaScript](web-javascript.md#composer)); a 409's state carries the
stored body in `data-sent-body` when the retried text starts with it.
Older pages use a plain form. Every feed/topic composer, including older pages,
submits a hidden server-issued `composer` generation scoped to the resolved
organisation/member/channel and actual posting topic. A failed message post
keeps that exact control value (or its absence) in both 422 fragments and full
pages; successful replacement or redirect rendering issues a fresh generation.
The post closes the submitted generation in the requested place
([`typing.md`](typing.md)). Composers also carry a hidden `idempotency_key`
(decision 37 (b)) with the same rule: a 422 keeps it, every other render has a
fresh one, including a 409's. The hidden sibling `#typing-form` carries only that
generation to the resolved posting topic's `/typing` route, with matching plain/enhanced POST URLs,
`hx-trigger="typing-input throttle:2s queue:none"`, `hx-sync="this:drop"` and
`hx-swap="none"`. Composer responses replace it out of band alongside the
state, keeping their generations equal. Each form coordinates its own requests;
signals cannot disable the message controls or block submission. Delegated input
and composition-end listeners trigger htmx for nonblank drafts, suppressing
composing input. They survive replacement without storing drafts or sending
requests themselves. Without scripts, this hidden form sends nothing.
The script scrolls to the newest entry on load, except when the URL's
fragment names a message on the page: a page opened from a message's URL
([decision 36](../decisions/36-one-message-has-a-stable-url.md), #1170)
keeps the browser's scroll to that message, which CSS `:target` highlights
with or without scripts. Focus left on the disabled send button returns to the
textarea. Scripts
stay outside the replaced section; CSP and htmx evaluation remain unchanged.

`message-history-v2.js` keeps the reader's place when "Load older"
prepends a page: the link selects the older page's `<li>` elements into
`#message-items` (`afterbegin`) and replaces `#load-older` out of band. The
script restores the distance from the pane's bottom after the swap and again
after settling, since local timestamps (`htmx:load`) can change the new
messages' height, then moves focus to the next control, or to the pane once
the oldest message is shown.

The page-only unread divider is a separate `<li>` immediately above the
snapshot's first unread message, using `accent` / `on-accent` for its
localised label and decorative `accent-line` rules. Load older carries `unread=<sequence>` alongside `before`,
including zero for no unread message; its responses reuse that sequence
after the visit's reading POSTs. Without a carried value, pages compute it
afresh. The parameter is accepted only on older-history URLs and affects
only presentation within the resolved scope. Shared message and move
components never include it, so live replacements leave it in place. Its
`data-event-seq` equals its message's, allowing move insertion to order all
list children. Moving that message out of a topic view removes its divider;
moving another message leaves it in place. Destination inserts add no divider.

`message-stream-v12.js` listens to the vendored SSE extension's
`htmx:sseBeforeMessage`. It passes the server's HTML to htmx for an append or
same-id replacement, settling synchronously so replay stays ordered and local
timestamps are ready before scrolling. Only appends update the separate polite
status with templ-rendered text; scrolling follows only when already at the
bottom, except for the sender's own post. A successful composer fragment's
`data-posted-message` names that message; the script keeps it as a pending
scroll outside the composer and scrolls to the item once it is present,
whether it arrived before the response, after it, or after a reconnect. Each
pending scroll is used once; failed posts create none. It updates `sse-connect`'s `after` after applied deliveries and transport checkpoints so replacement
EventSources resume from the received id; native reconnects use Last-Event-ID.
`messages-moved` carries a templ-rendered list of replacements. The script
replaces feed IDs, removes source-topic IDs or inserts destination IDs in
sequence order within the loaded range. The history control's bound is zero
when no older history remains, including an empty page, admitting every moved
item; otherwise it is the oldest loaded sequence. It changes only on Load older;
moves leave announcements and paging alone. While a Load older request is in
flight, the script retains move payloads in delivery order. After both the
history items and control are swapped, it reapplies those moves against the
new bound, removing stale source items and inserting missing destination items.
It then drops the payloads; failed, aborted or unswapped responses drop them too.
Replacements clear checked state
and refresh the branch form's source constraints. SSE swaps have no request
target, so composer/history focus handlers ignore them.

## Languages

`internal/web/i18n` embeds the English and Japanese TOML
catalogues (go-i18n). `cmd/ribbitto` creates one catalogue service for the
process; middleware on HTML routes puts a localizer in the request context.
A valid `lang` cookie wins over `Accept-Language`; the matcher's index picks
a supported catalogue, defaulting to English, and HTML responses add
`Accept-Language` and `Cookie` to `Vary`. Templates get text only through
`i18n.T(ctx, "message.id")` and the page language through `i18n.Language`.
A missing message falls back to English, then to the ID, and is logged once
per language and ID.
