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

Requires Go (see `go.mod` for the version) and Docker Compose for PostgreSQL 18.

```sh
cp .env.example .env
set -a; . ./.env; set +a    # export configuration; the binary does not load .env
make db-up                 # starts PostgreSQL and waits for health
go run ./cmd/ribbitto migrate up
go run ./cmd/ribbitto       # or: go run ./cmd/ribbitto serve
make check                 # checks formatting, vets, lints, builds and tests
make db-down               # stops PostgreSQL; keeps the named data volume
```

The server listens at http://localhost:8080/healthz and never migrates on
startup. Use `ribbitto migrate up|down|status` with a built binary; `down`
reverts one migration. See [database development](docs/database.md) for
configuration, migration ownership and integration tests.

## Contributing

The project is maintained by one person and developed with AI coding tools;
see [`AGENTS.md`](AGENTS.md) for the conventions that apply to every change
and [`docs/workflow.md`](docs/workflow.md) for how work flows.
Issues and pull requests are welcome, in English.

## Licence

[MIT](LICENSE)
