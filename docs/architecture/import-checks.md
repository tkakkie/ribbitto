# Import checks

How `make check` enforces the import rules of [packages and allowed imports](packages.md). The [architecture index](README.md) lists
the other files.

A package's sub-packages may import each other, apart from the store,
wiring and fixture rules below. The [module manifest](../../module_imports_test.go)
is the source of truth for module paths, edges and fixture exceptions. Its Go tests
check imports per source file and reject missing paths, duplicates, unknown edge
names and unlisted module-shaped directories. Depguard keeps the other boundaries;
both fail `make check` on a violating import:

- each module's and package's imports **within `internal/`** (other
  imports from this module are listed in the table by convention, not
  enforced);
- the `view` rule forbids `internal/web/view` from importing `internal/identity`, `internal/org` or `internal/conversation`, including sub-packages (see [web layers](web-layers.md));
- `identity`, `org`, `conversation` and `realtime` cannot import
  `github.com/a-h/templ` (including sub-packages) or `html/template`;
- `kernel` imports nothing internal; `platform` only `kernel`;
  `web` imports a module's root, never its store or wiring;
- a module's wiring (`identitypg`, `realtimepg`, `orgpg`, `conversationpg`) is imported only by `cmd/*` and tests,
  and its store only by its wiring and the store's own tests;
- the manifest permits fixture imports only from test files and higher fixture
  packages; it fixes their root imports and direction, and keeps them off
  stores and wiring (decision 29);
- only stores (`**/internal/postgres/**`) import `platform/postgres/pgxbridge`;
  `make lint-fixtures` (part of `make check`) proves a module root is
  rejected and a store accepted;
- `db/migrations` may be imported only by `internal/platform/postgres` and
  its packages, their tests included — **the rule also applies to test
  files**, so every other test migrates to a version through
  `pgtest.NewMigrator`;
- otherwise test files may import any package, but the store and bridge
  rules above bind them too (only the platform's `tx_test.go` is exempt from
  the bridge rule).

This file, the manifest and `.golangci.yml` must agree; change them together.

`make check` also requires a `doc.go` in every directory under `internal/`
that contains non-test Go files, including generated packages, as specified
in `AGENTS.md`. Fixtures under `testdata/` are excluded.
