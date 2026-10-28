# Local development

Running [ribbitto](../README.md) locally and running its checks; see the
[documentation index](README.md) for the other guides.

## Development

Requires Go (see `go.mod` for the version) and Docker Compose for PostgreSQL 18.

```sh
cp .env.example .env
set -a; . ./.env; set +a    # export configuration; the binary does not load .env
make db-up                 # starts PostgreSQL and waits for health
make migrate               # DIRECTION=up|down|status
make seed ARGS='-messages 100' # optional development fixtures
go run ./cmd/ribbitto       # http://localhost:8080
make generate              # regenerates committed templ Go files
make css                   # rebuilds committed, minified Tailwind CSS
make dev                   # watches templ and CSS; restarts the server
make check                 # checks formatting, vets, lints, builds and tests (with -race)
make vuln                  # govulncheck: known vulnerabilities reachable from our code
make deps                  # regenerates docs/dependencies.md after an import change
make api                   # regenerates docs/api/ after a module API or doc-comment change
make db-down               # stops PostgreSQL; keeps the named data volume
```

Use `/readyz` for a readiness healthcheck: it pings PostgreSQL with a one-second
timeout and returns 503 if unavailable or shutting down. Use `/healthz` for
liveness; it always returns 200 while the process can answer requests.

For parallel worktrees, optionally run `make ai-env` after `make db-up`.
It migrates a separate dev database and allocates app/metrics ports in
`.env.local`; the Make targets above then use those values instead of
`RIBBITTO_DATABASE_URL`. Start other commands with
`./bin/ai-db run <command>` (built by `make bin/ai-db`) to use that configuration.
Commands use the worktree root even from a subdirectory. Git worktree IDs
keep databases across moves and symlink spellings. Local database names and
ports must match the registry; reruns keep free ports and replace busy ones.
`make ai-health` checks the shared admin connection without printing credentials.
`make ai-env-clean` drops this worktree's dev database and releases its ports; it also
drops those of registry entries whose worktrees are gone.
See [worktree environments](worktree-env.md) for configuration overrides and capacity measurement.
Startup never migrates; schemas below its newest migration fail with
`ribbitto migrate up`; equal/newer allow binary rollback.

Set `RIBBITTO_LOG_FORMAT=json` for JSON logs; empty keeps the default; other
values prevent startup.

Behind a reverse proxy, set `RIBBITTO_TRUSTED_PROXIES` to the address of the
proxy that connects to ribbitto, as a `/32` or `/128` CIDR (for example
`10.0.0.2/32`; several are comma-separated), so that the sign-in, sign-up and
setup rate limits see each client's address instead of the proxy's. List only
proxies, never a network that also contains clients, and make the proxy append
the peer it saw to `X-Forwarded-For` or overwrite the header. See the
[reverse-proxy contract](architecture/rate-limits.md#reverse-proxies) for the full rules
and for setups with several proxies. Left empty (the default), ribbitto ignores
`X-Forwarded-For`, and all clients behind the proxy share one limit.

The session cookie is always `Secure` (there is no switch to turn that off:
it would be too easy to leave off in production). Browsers that treat
`http://localhost` as a secure context keep such cookies over plain HTTP;
Chromium does for both `localhost` and `127.0.0.1` (checked when the cookie
was added). If your browser drops the cookie, use a Chromium-based browser
for local development or serve through HTTPS. Use `ribbitto migrate up|down|status` with a built binary; `down`
reverts one migration. See [database development](database.md) for
configuration and migration ownership, and [database tests](database-tests.md)
for integration tests.

`make dev` builds CSS before starting the server. It runs the pinned templ
and Tailwind watchers together, restarting Go when templates or Go source
change. It sets `RIBBITTO_DEV_ASSETS=web/static` to serve assets from disk
without caching and recompute the stylesheet URL's content hash on each page
request. Reload on edits; CSS needs no server restart.
`cmd/dev` stops watchers and server on Ctrl-C, SIGTERM or watcher exit.

`make css` and `make lint` use [`tools/download`](../tools/download/main.go)
for SHA-256 checked tools (macOS ARM64 and Linux x64). Node/npm are only
for type checking, unit tests and dependency management; see [setup](architecture/web-toolchain.md#versions-and-installation--1410-1415).
Generated Go, CSS and vendored scripts are committed, so `go build` needs
neither templ nor Tailwind. CSS class detection is limited to `.templ` files
so local notes and tools do not change the output.

### Checks

`make check` must pass before a pull request; for another AI's code,
green draft CI must pass before review instead
([handoff](workflow/running-other-ai.md#handoff-of-another-ais-code)).
It runs `check-static`, `check-test`, `check-tools`, `check-web`, `test-web`. CI runs
`check-static` and `check-tools` in `checks`, each `testshard` shard
in a parallel `test` matrix job, `rest` in `test-rest`,
and TypeScript in `web-types` (`.github/workflows/ci.yml`).
[`testshard -run`](../tools/testshard/main.go) owns test packages, selectors and flags.
`make check` checks formatting and internal `doc.go` files via
[`tools/formatcheck`](../tools/formatcheck/main.go), vets, lints,
builds, runs Go tests with `-race` (`-p 2` by default;
[worktree-env.md](worktree-env.md)), and checks the import
graph, module API summaries and documents. `tools/sourcecheck` enforces
[product vocabulary](domain/vocabulary.md) and
[UI colour rules](ui-colours.md#colour-tokens). In `docs/`, `AGENTS.md`, `README.md` and `DECISIONS.md`,
paths in backticks must exist. Placeholders and globs are skipped;
brace sets expand; `:line` suffixes and trailing slashes are accepted.
An existing path wins. Otherwise, a final dot-separated known extension
(`go`, `md`, `sql`, `templ`, `toml`, `js`, `css`, `json`, `yml`,
`yaml`, `txt`, `sh`) marks a missing file. Only other spans can be package
selectors such as `package.Identifier` or `package.Type.Method`: every name
after the package must be a Go identifier, exported or not, and the
package directory must hold a regular `.go` file.
Historical decision records skip this path check; links and
anchors are checked.
Markdown images must have non-empty rendered alt text and resolve relative
to their containing document, after URL decoding and path normalisation,
to regular PNG or WebP files directly in docs/images/ (no subdirectories).
External URLs and absolute paths are refused; extensions are case-sensitive.
Each image file is limited to 300 KiB (307,200 bytes). Generated schema
Markdown is exempt from image-link checks.

`make prepush` is a quick subset for follow-up pushes: it
regenerates templates, SQL and CSS and fails if that changes a file, then
checks the API and dependency manifests, documents and lint, stopping at
the first failure. It does not run Go or templ formatting, the `doc.go` check,
`go vet`, `sourcecheck` or the tests, which `make check` keeps, nor the schema
docs or `make vuln`, which CI runs separately.

The Argon launcher's Go tests run with other command tests in
`make check`. CI's `vuln` job runs
`make vuln` (pinned `govulncheck` on the application module),
failing if a known vulnerability is statically reachable from our code.
It needs the network, so it is not part of `make check`. When it fails:
bump the dependency in its own pull request, or upgrade Go for a
standard-library finding; without a fix, the maintainer decides.

CodeQL (`.github/workflows/codeql.yml`) runs on `main` and weekly, not on
pull requests; alerts are in the Security tab.

### README screenshot

`docs/images/screenshot.webp` is one image: the desktop screen on the left,
the phone screen on the right, both of the same state. Refresh it when the
look changes, with development data only (no real people, no secrets):

1. On an empty database run `make db-up`, `make migrate` and
   `make seed ARGS='-messages 12'`; the command prints this run's password
   ([seed data](seed-data.md)). Start the server (`go run ./cmd/ribbitto`).
2. In a Chromium-based browser, English UI (`Accept-Language: en`) and light
   theme, sign in at `/signin` as `mira@example.test`.
3. Open the `chorus` topic of `general`: the sidebar then shows unread counts
   for `dev` (12), `general` (62), its topics `garden-time` (1) and
   `rooftop-garden` (61), `incident-garden` (12) and `random` (12). Opening
   a topic reads only that topic; opening the channel feed would reset counts.
4. Capture the same page twice with device emulation: desktop at 1000 × 844 and
   phone at 390 × 844 (touch, mobile layout), both with device pixel ratio 2.
5. Place both captures side by side on a light grey background, 24 px
   padding and gaps, rounded corners, 1462 × 892 CSS pixels at ratio 2 (2924 × 1784
   pixels), and save as WebP of at most 300 KiB. View it at 830 px wide, the
   README column on GitHub: channel names, topics and counts must stay
   readable; if not, use the desktop capture alone.
