# Content Security Policy

Content Security Policy and script delivery for [rendering and assets](rendering.md).

## Content Security Policy

Each HTML response requires a fresh base64-encoded 32-byte `crypto/rand`
nonce on every shared-layout script. `internal/web/middleware/security.go`
defines the policy:

```text
default-src 'self'; script-src 'nonce-<nonce>'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'
```

Even same-origin scripts require the nonce: `script-src` has no `'self'`.
This is defence in depth against HTML injection. The middleware also sends:

- `X-Content-Type-Options: nosniff`
- `Referrer-Policy: same-origin`
- `Cross-Origin-Opener-Policy: same-origin`
- `Permissions-Policy: camera=(), microphone=(), geolocation=(), payment=(), usb=()`

These apply to SSE too; no separate JSON routes exist. `/static/`,
`/healthz` and `/readyz` bypass the middleware.
The static handler sends `X-Content-Type-Options: nosniff`
and `Content-Security-Policy: default-src 'none'; sandbox` on every response,
found or not, including the explicit 405 (`Allow: GET, HEAD`) for any other
method: an SVG opened directly is a document on this origin, so
it runs nothing and loads nothing. Pages are unaffected, because a file used
as `<img>`, `<use>`, a script or a stylesheet does not apply its own policy;
`TestBrowser/static-headers` checks that icons and illustrations
still paint. Caddy owns `Strict-Transport-Security` in deployment.

The layout's `htmx-config` meta tag sets `allowEval: false`, `allowScriptTags: false`
and `includeIndicatorStyles: false`, disabling attribute evaluation, swapped
scripts and inline indicator styles. Keep scripts and styles external;
never use inline scripts, `hx-on` or other attribute scripts.

The same meta tag sets `responseHandling` to htmx's defaults (`204`: no
swap; `[23]..`: swap; `[45]..`: no swap, error), with
409 and 422 entries with `swap: true, error: false` before `[45]..`. The first
matching entry wins: composer errors replace `#message-composer-state`, branch
errors replace `#branch-feedback`. New htmx requests inherit both rules
([`web-htmx.md`](web-htmx.md)). Other status codes retain their defaults.

Load the content-hashed `web/src/message-time.ts` bundle under `/static/dist/`
with the response nonce on every channel page, empty or not: htmx does not run
scripts in swapped fragments. Render `<time datetime>` in UTC, truncating only
the attribute to milliseconds, preserving stored timestamps and visible UTC
fallback text. Localise the initial page and new `htmx:load` subtrees once,
in the page language; invalid dates and disabled JavaScript retain the fallback.
Reserve space for UTC and local text at all widths so localisation neither
moves adjacent content from `desk` nor shrinks the cell below it; any hidden
sizing copy has empty alternative text
([`ui-components.md`](../ui-components.md#shared-components)).
Hashed bundles are immutable; the fixed-name manifest is not public.
Unconverted application scripts retain versioned URLs.

Channel pages load the content-hashed composer bundle from `/static/dist/`
(built from `web/src/composer.ts`) with the response nonce, outside swaps, for
[composer growth](../ui-composer.md) and
[quote focus and locking](web-javascript.md#javascript).
`requestSubmit` follows the [send-key preference](identity.md). IME (`isComposing`, keyCode 229, or
100 ms after composition ends) never sends. Ordinary and pending sending,
fallbacks and failures follow the [composer contract](web-composer.md#composer).
An ordinary form in flight blocks composer submits; valid pending sends queue.
While a send is in flight, only the send button is disabled, `hx-sync="this:drop"` drops another send, and the `sending:`
variant (htmx's `htmx-request` class) shows a polite "Sending…" status over the
help, which keeps its space; the textarea stays usable.
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
signals cannot disable the message controls or block submission. Nonblank drafts
trigger typing signals, suppressing composing input; [draft restore](web-composer.md#drafts)
sends none. Without scripts the form is inert.
Without scripts CSS starts the latest page at the newest entry, and with
scripts the composer scrolls there on load unless the URL's fragment names a
message on the page ([decision 36](../decisions/36-one-message-has-a-stable-url.md)),
keeping the browser's scroll to that `:target`-highlighted message. Focus left on the disabled send button returns to the
textarea.

`web/src/message-history.ts` keeps the reader's place when "Load older"
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

`web/src/message-stream.ts` listens to the vendored SSE extension’s
`htmx:sseBeforeMessage` and passes server HTML to htmx for append or same-ID replacement,
settling synchronously to order replay and localise
timestamps before scrolling. Only appends update the separate polite
status with templ-rendered text; scrolling follows only when already at the
bottom, except for the sender's own post. A successful composer fragment's
`data-posted-message` keeps a pending scroll outside the composer until arrival,
before or after the response or a reconnect. Each scroll runs once; failures
create none. It updates `sse-connect`'s `after` after applied deliveries and transport checkpoints so replacement
EventSources resume from the received id; native reconnects use Last-Event-ID.
On `reset` it closes the source and holds reload for unsent text ([guard](web-javascript.md#javascript)).
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
