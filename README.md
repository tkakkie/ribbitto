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
go run ./cmd/ribbitto       # serves http://localhost:8080/ (or: ... serve)
make generate              # regenerates committed templ Go files
make css                   # rebuilds committed, minified Tailwind CSS
make dev                   # watches templ and CSS; restarts the server
make check                 # checks formatting, vets, lints, builds and tests (with -race)
make vuln                  # govulncheck: known vulnerabilities reachable from our code
make deps                  # regenerates docs/dependencies.md after an import change
make db-down               # stops PostgreSQL; keeps the named data volume
```

The server listens at http://localhost:8080/healthz and never migrates on
startup.

Behind a reverse proxy, set `RIBBITTO_TRUSTED_PROXIES` to the address of the
proxy that connects to ribbitto, as a `/32` or `/128` CIDR (for example
`10.0.0.2/32`; several are comma-separated), so that the sign-in, sign-up and
setup rate limits see each client's address instead of the proxy's. List only
proxies, never a network that also contains clients, and make the proxy append
the peer it saw to `X-Forwarded-For` or overwrite the header. See the
[reverse-proxy contract](docs/architecture/rate-limits.md#reverse-proxies) for the full rules
and for setups with several proxies. Left empty (the default), ribbitto ignores
`X-Forwarded-For`, and all clients behind the proxy share one limit.

The session cookie is always `Secure` (there is no switch to turn that off:
it would be too easy to leave off in production). Browsers that treat
`http://localhost` as a secure context keep such cookies over plain HTTP;
Chromium does for both `localhost` and `127.0.0.1` (checked when the cookie
was added). If your browser drops the cookie, use a Chromium-based browser
for local development or serve through HTTPS. Use `ribbitto migrate up|down|status` with a built binary; `down`
reverts one migration. See [database development](docs/database.md) for
configuration, migration ownership and integration tests.

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

### Checks

`make check` is what CI runs and what must pass before a pull request: it
checks formatting (`gofmt` for Go, `templ fmt` for templates), vets, lints,
builds, runs the Go tests with the race detector, and checks the import
graph and the documents. CI also runs
`make vuln`, which runs the pinned `govulncheck` over the application module
and fails when a known vulnerability is statically reachable from our code.
It needs the network, so it is not part of `make check`. When it fails:
bump the dependency in its own pull request, or upgrade Go for a
standard-library finding; if no fix exists yet, the maintainer decides.

## First-run setup

After running migrations, set `RIBBITTO_SETUP_TOKEN` to at least 32 random
characters before starting the server, then open `/setup`. Enter that token,
the organisation name and URL slug, and the owner's display name, email and
password. Successful setup signs the owner in and redirects to `/`.
An unset or empty token disables setup; a non-empty token shorter than 32
characters prevents startup. After setup, both GET and POST `/setup` return
404 even if the token remains in the environment.

Set `RIBBITTO_SIGNUP=on` to let people register at `/signup` after setup.
They join the setup organisation as members and are signed in automatically.
`off`, empty or unset disables sign-up (GET and POST return 404); any other
value prevents startup. Sign-in links to registration only while it is open.

## Documentation

- [Architecture](docs/architecture/README.md) — packages, allowed imports, request and real-time flow
- [Domain](docs/domain/README.md) — glossary, entities, invariants, unread rules
- [UI](docs/ui.md) — design direction, tokens, contrast
- [Decisions](DECISIONS.md) — what was decided and why
- [Roadmap](docs/roadmap.md) · [AI development workflow](docs/workflow/README.md) · [Database development](docs/database.md)

## Contributing

The project is maintained by one person and developed with AI coding tools;
see [`AGENTS.md`](AGENTS.md) for the conventions that apply to every change
and [`docs/workflow/`](docs/workflow/README.md) for how work flows.
Issues and pull requests are welcome, in English.

## Licence

[MIT](LICENSE)
