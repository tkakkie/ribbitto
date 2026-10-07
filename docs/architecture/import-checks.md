# Import checks

How `make check` enforces the import rules of [packages and allowed imports](packages.md). The [architecture index](README.md) lists
the other files.

A package's sub-packages may import each other, apart from the store,
wiring and fixture rules below. depguard in `.golangci.yml`
enforces the part of [the package table](packages.md) that matters most, and a violating import
fails `make check`:

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
- the `fixtures` rule lets only tests and higher fixture packages import the
  test-only `identitytest`, `orgtest` and `conversationtest` (decision 29);
  `identityfixture`, `orgfixture` and `conversationfixture` let each import
  only `kernel`, its own root, the roots it needs for types and lower
  fixture packages (`conversationtest` → `orgtest` → `identitytest`), and
  the store, wiring and bridge rules keep them off stores and wiring;
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

This file and `.golangci.yml` must agree; change them together.

Staticcheck also runs on generated code, including templ output; the
`_templ.go` exclusions apply only to errcheck and revive. `make lint-fixtures`
proves staticcheck rejects ignored pure-function results in both handwritten
Go and generated templates.
For components that use neither `ctx` nor children, add `{{ _ = ctx }}` to avoid
SA4006 on templ's unused `ClearChildren` result, because golangci-lint ignores
templ's `//lint:file-ignore SA4006` directive.

Nilerr runs with no exclusions and rejects returning a nil error after
checking that an error is non-nil, unless the block uses the error (for
example, `log.Print(err); return nil` counts as handling it). It also reports
the opposite case, `if err == nil { return err }`. `make lint-fixtures`
asserts the non-nil-error finding in a tagged Go fixture, so disabling
nilerr fails the check.

`make check` also requires a `doc.go` in every directory under `internal/`
that contains non-test Go files, including generated packages, as specified
in `AGENTS.md`. Fixtures under `testdata/` are excluded.
