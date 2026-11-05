# Rendering and assets

Embedded assets use immutable caching and a SHA-256 stylesheet URL.
Development (`make dev`, `RIBBITTO_DEV_ASSETS=bin/dev-dist`) serves rebuilt CSS,
bundles and `.js.map` source maps without restarting.

Fluent icons follow the [UI convention](../ui-code.md#icons) and
[Content Security Policy](csp.md#content-security-policy).
Channel illustrations use versioned URLs, a stable ID-based fallback for NULL
or unknown keys, empty alt text and reserved space (24 px in sidebar rows,
32 px in the header), asynchronous decoding and lazy sidebar loading.

## Languages

A valid `lang` cookie wins over `Accept-Language`, defaulting to English.
HTML responses vary on `Accept-Language` and `Cookie`. Templates use
`i18n.T(ctx, "message.id")` and `i18n.Language`. Missing messages fall back to
English, then the ID, and are logged once per language and ID.
