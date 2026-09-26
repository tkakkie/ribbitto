# Database development

**Keep it current:** update this file when configuration, the migration
workflow or the integration-test setup changes.

`RIBBITTO_DATABASE_URL` configures both the server and the explicit
`migrate up|down|status` command. The server checks connectivity but never
applies migrations. `.env.example` contains the local Compose URLs; export
them into the shell as shown in the README. `.env` is ignored by Git.

`make db-up` starts PostgreSQL 18 on loopback port 5432 and waits for its
health check. Its password is for development only. `make db-down` stops
it without deleting the named volume at `/var/lib/postgresql`.

`db/migrations` holds numbered goose SQL files embedded in the binary.
It is owned by `internal/infra/postgres`; only that package and
`cmd/ribbitto` may import it. The adapter uses a goose provider and pgx's
`database/sql` adapter without global goose configuration. The first
migration creates `organization`; goose also maintains its version table.

Integration tests use `RIBBITTO_TEST_DATABASE_URL`, an admin connection to
the `postgres` database as the `postgres` superuser. Each test run creates
a uniquely named database, closes its connections and drops it with
`WITH (FORCE)`. Do not point this variable at a production server.
Tests skip only when the variable is unset; an empty or broken value fails.
`RIBBITTO_REQUIRE_DB=1` also makes an unset URL fail, as required in CI.

```sh
RIBBITTO_REQUIRE_DB=1 go test -count=1 -v ./internal/infra/postgres
env -u RIBBITTO_TEST_DATABASE_URL -u RIBBITTO_REQUIRE_DB go test -count=1 -v ./internal/infra/postgres
```
