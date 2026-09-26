# ribbitto

A small, self-hostable team chat with a frog on the logo.

> **Status: early development.** Nothing works yet. See the
> [roadmap](docs/roadmap.md) and the pinned Status issue.

## What it will be

- Channels and messages that arrive in real time, with unread counts,
  presence and typing indicators.
- Server-rendered HTML with htmx: one Go binary, no JavaScript build step.
- English and Japanese UI.
- Easy to self-host: a container image and a Compose file with PostgreSQL
  and Caddy.

## Stack

Go, `net/http`, templ, htmx (Server-Sent Events), Tailwind CSS,
PostgreSQL with pgx, sqlc and goose.

## Development

Requires Go (see `go.mod` for the version).

```sh
go run ./cmd/ribbitto        # serves http://localhost:8080/ and /healthz
make generate              # regenerates committed templ Go files
make css                   # rebuilds committed, minified Tailwind CSS
make dev                   # watches templ and CSS; restarts the server
make check                 # checks formatting, vets, lints, builds and tests
```

`make dev` builds CSS before starting the server. It runs the pinned templ
and Tailwind watchers together, restarting Go when templates or Go source
change. It sets `RIBBITTO_DEV_ASSETS=web/static` to serve assets from disk
without caching and recompute the stylesheet URL's content hash on each page
request. Reload the browser after an edit; rebuilt CSS needs no server restart.
Ctrl-C stops the watchers and server.

The standalone Tailwind CLI is downloaded and SHA-256 checked by `make css`
(macOS ARM64 and Linux x64). No npm or JavaScript build step is needed.
Generated Go, CSS and vendored scripts are committed, so `go build` needs
neither templ nor Tailwind. CSS class detection is limited to `.templ` files
so local notes and tools do not change the output.
Local PostgreSQL tooling arrives later in M0.

## Contributing

The project is maintained by one person and developed with AI coding tools;
see [`AGENTS.md`](AGENTS.md) for the conventions that apply to every change
and [`docs/workflow.md`](docs/workflow.md) for how work flows.
Issues and pull requests are welcome, in English.

## Licence

[MIT](LICENSE)
