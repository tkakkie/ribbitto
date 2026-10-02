# Rendering and assets

templ components live in `internal/web/view`. Static assets are embedded
from `web/static` into the binary; the stylesheet URL carries the SHA-256
of the embedded CSS, so assets can be cached as immutable. In development
(`make dev`, `RIBBITTO_DEV_ASSETS=web/static`) assets are read from disk and
the hash is recomputed per request, so rebuilt CSS appears without a
restart.

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
middleware. Caddy owns `Strict-Transport-Security` in deployment.

The layout's `htmx-config` meta tag sets `allowEval: false`,
`allowScriptTags: false` and `includeIndicatorStyles: false`. htmx cannot
evaluate attribute scripts, execute scripts from swapped fragments, or
inject its default inline indicator stylesheet. Do not use inline scripts,
`hx-on`, or other attribute scripts; keep JavaScript and styles in external
assets.

The same meta tag sets `responseHandling` to htmx's defaults (`204`: no
swap; `[23]..`: swap; `[45]..`: no swap, error), with
409 and 422 entries with `swap: true, error: false` before `[45]..`. The first
matching entry wins: composer errors replace `#message-composer`, branch
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
when JavaScript is disabled.
Application script URLs are versioned because static assets are immutable.

`message-composer-v3.js` also loads with the response nonce on channel pages.
One key-to-command function maps Enter to `send` and Shift+Enter to `newline`,
ignoring IME composition (`isComposing` or keyCode 229); one submit function
uses the native form submission path through `requestSubmit`. The latest-page form uses
htmx to replace only `#message-composer`, disabling its controls in flight.
Older pages use a plain form. The script scrolls to the newest entry on load
and focuses the composer after its replacement. Scripts
stay outside the replaced section; CSP and htmx evaluation remain unchanged.

`message-history-v2.js` keeps the reader's place when "Load older messages"
prepends a page: the link selects the older page's `<li>` elements into
`#message-items` (`afterbegin`) and replaces `#load-older` out of band. The
script restores the distance from the pane's bottom after the swap and again
after settling, since local timestamps (`htmx:load`) can change the new
messages' height, then moves focus to the next control, or to the pane once
the oldest message is shown.

`message-stream-v4.js` listens to the vendored SSE extension's
`htmx:sseBeforeMessage`. It passes the server's HTML to htmx for an append or
same-id replacement, settling synchronously so replay stays ordered and local
timestamps are ready before scrolling. Only appends update the separate polite
status with templ-rendered text; scrolling follows only when already at the
bottom, except for the sender's own post. A successful composer fragment's
`data-posted-message` names that message; the script keeps it as a pending
scroll outside the composer and scrolls to the item once it is present,
whether it arrived before the response, after it, or after a reconnect. Each
pending scroll is used once; failed posts create none. It updates `sse-connect`'s `after` after each delivery so replacement
EventSources resume from the received id; native reconnects use Last-Event-ID.
`messages-moved` carries a templ-rendered list of replacements. The script
selects each loaded stable ID for an htmx replacement, leaving unloaded items,
announcements and the paging control alone. Replacements clear checked state
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
