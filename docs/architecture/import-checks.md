# Import checks

How `make check` enforces the import rules of [packages and allowed imports](packages.md). The [architecture index](README.md) lists
the other files.

Sub-packages of a layer may import each other. depguard in `.golangci.yml`
enforces the part of [the package table](packages.md) that matters most, and a violating import
fails `make check`:

- each layer's imports **within `internal/`** (other imports from this module
  are listed in the table by convention, not enforced per layer);
- the `view` rule forbids `internal/web/view` from importing `internal/identity`, `internal/org`, `internal/conversation` or `internal/infra`, including sub-packages (see [web layers](web-layers.md));
- `domain`, `identity`, `org`, `conversation`, `infra/postgres` and `realtime` cannot import
  `github.com/a-h/templ` (including sub-packages) or `html/template`;
- `kernel` imports nothing internal; `platform` only `kernel`;
  `infra/postgres` and `web` import a module's root, never its store or
  wiring;
- a module's wiring (`identitypg`, `realtimepg`, `orgpg`, `conversationpg`) is imported only by `cmd/*` and tests,
  and its store only by its wiring and the store's own tests;
- only stores (`**/internal/postgres/**`) import `platform/postgres/pgxbridge`;
  `make lint-fixtures` (part of `make check`) proves a module root is
  rejected and a store accepted;
- `db/migrations` may be imported only by `internal/platform/postgres` and
  `cmd/ribbitto` — **this also applies to test files**, apart from the
  topic backfill test in `internal/conversation/internal/postgres/topic_test.go`,
  `internal/realtime/internal/postgres/event_log_test.go`,
  `internal/org/internal/postgres/member_handle_test.go` and
  conversation's `default_channel_backfill_test.go`, until step 5;
- otherwise test files may import any package, but the store and bridge
  rules above bind them too (only the platform's `tx_test.go` is exempt from
  the bridge rule).

This file and `.golangci.yml` must agree; change them together.

`make check` also requires a `doc.go` in every directory under `internal/`
that contains non-test Go files, including generated packages, as specified
in `AGENTS.md`. Fixtures under `testdata/` are excluded.
