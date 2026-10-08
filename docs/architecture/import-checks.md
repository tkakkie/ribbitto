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

Staticcheck also runs on generated code, including templ output; the
`_templ.go` exclusions apply only to errcheck and revive. `make lint-fixtures`
proves staticcheck rejects ignored pure-function results in both handwritten
Go and generated templates.
For components that use neither `ctx` nor children, add `{{ _ = ctx }}` to avoid
SA4006 on templ's unused `ClearChildren` result, because golangci-lint ignores
templ's `//lint:file-ignore SA4006` directive.

Bodyclose requires HTTP response bodies to be closed, including in tests.
Acceptance helpers read and close ordinary responses before returning status,
request metadata, headers, cookies and body text; callers never own an open body.
`make lint-fixtures` proves an unclosed response is rejected.

`sqlclosecheck` runs without exclusions and rejects SQL/pgx rows and statements
that are neither closed nor used. Any other use of the rows, such as
`rows.Next()` or `rows.Err()`, satisfies it: lint does not catch a missing
Close once rows are used, including an early return from a scan loop. Keep
`defer rows.Close()` right after the query's error check. `make lint-fixtures`
proves it rejects unused, unclosed pgx rows; disabling the linter fails that
assertion.

Nilerr runs with no exclusions and rejects returning a nil error after
checking that an error is non-nil, unless the block uses the error (for
example, `log.Print(err); return nil` counts as handling it). It also reports
the opposite case, `if err == nil { return err }`. `make lint-fixtures`
asserts the non-nil-error finding in a tagged Go fixture, so disabling
nilerr fails the check.

`make check` also requires a `doc.go` in every directory under `internal/`
that contains non-test Go files, including generated packages, as specified
in `AGENTS.md`. Fixtures under `testdata/` are excluded.
