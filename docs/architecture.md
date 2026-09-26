# Architecture

`cmd/ribbitto` wires the HTTP handler into the server. `internal/web` owns
routing and HTML rendering, with shared templ components in `internal/web/view`.
Handlers call use cases and render their plain results; other layers do not
produce HTML.

`internal/web/i18n` embeds English and Japanese TOML catalogues using go-i18n.
The command creates one catalogue service for the process; HTML middleware
puts a localizer in the request context. A valid `lang` cookie takes precedence
over `Accept-Language`; matching selects a supported catalogue by index and
defaults to English. HTML responses vary on both headers. Templates use `T`
for messages and `Language` for `<html lang>`. Missing messages fall back to
English, then the ID, logging once per language/ID through the shared service.

`internal/web` may import `web/static`, which embeds the built stylesheet and
vendored JavaScript. The stylesheet URL includes the embedded bytes' SHA-256
so a new build gets a new cache key. Assets are served under `/static/` with
immutable caching; changing CSS requires rebuilding the binary.

For development, `make dev` sets `RIBBITTO_DEV_ASSETS=web/static`. The handler
serves assets from that directory without caching and recomputes the stylesheet
hash on each page request, so CSS rebuilds appear on reload without restarting
Go. With the variable unset, assets stay embedded and the hash is computed only
at startup.
