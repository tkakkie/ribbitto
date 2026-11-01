# Rendering and assets

templ components live in `internal/web/view`. Assets embedded from
`web/static` are cached as immutable; the stylesheet URL carries the CSS's SHA-256.
In development (`make dev`, `RIBBITTO_DEV_ASSETS=web/static`), disk assets,
a per-request CSS hash and per-request `dist/manifest.json` reloads make
rebuilt CSS, script bundles and `.js.map` source maps appear without restarting.

Fluent icons use an embedded, versioned SVG sprite in `web/static/vendor`
through `Icon` ([UI convention](../ui-code.md#icons)). Its same-origin `<use>`
references need no Content Security Policy change.

Channel illustrations ship from the embedded `channel-images-v1` directory
with versioned URLs. Handlers resolve stored keys or choose a stable
ID-based fallback for NULL or unknown keys, then pass the URL to the shared
view component. It reserves 24 px in sidebar rows and 32 px in the header,
uses empty alt text and asynchronous decoding, and lazy-loads sidebar images.

See [Content Security Policy and script delivery](csp.md#content-security-policy).

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
