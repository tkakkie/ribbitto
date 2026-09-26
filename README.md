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
go run ./cmd/ribbitto        # serves http://localhost:8080/healthz
go build ./... && go vet ./...
```

More tooling (`make dev`, `make check`, a local PostgreSQL) arrives with the
first milestone.

## Contributing

The project is maintained by one person and developed with AI coding tools;
see [`AGENTS.md`](AGENTS.md) for the conventions that apply to every change
and [`docs/workflow.md`](docs/workflow.md) for how work flows.
Issues and pull requests are welcome, in English.

## Licence

[MIT](LICENSE)
