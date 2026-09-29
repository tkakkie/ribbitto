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

Message timestamps use `message-time-v1.js`, loaded with the response nonce
on every channel page, empty or not, because htmx does not run scripts in
swapped fragments. It formats `<time datetime>` values in browser local time
on load and on each `htmx:load`, retaining the server's UTC fallback when
JavaScript is disabled.
Application script URLs are versioned because static assets are immutable.

`message-composer-v1.js` also loads with the response nonce on channel pages.
One key-to-command function maps Enter to `send` and Shift+Enter to `newline`,
ignoring IME composition (`isComposing` or keyCode 229); one submit function
uses the native form submission path through `requestSubmit`. The form uses
htmx to select and replace `#conversation`, disabling its controls in flight.
The script opts that target into 422 swaps for field errors and scrolls the
message pane to the newest entry on load and after settling a swap. Scripts
stay outside the replaced section; CSP and htmx evaluation remain unchanged.

`message-history-v1.js` keeps the reader's place when "Load older messages"
prepends a page: the link selects the older page's `<li>` elements into
`#message-items` (`afterbegin`) and replaces `#load-older` out of band. The
script restores the distance from the pane's bottom and moves focus to the
next control, or to the pane once the oldest message is shown.

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
