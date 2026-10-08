# 29. Test fixtures live with the module that owns their tables

**Decided** (#580, amending [decision 26](26-modules-by-feature-layout-seams-and-order.md)'s
layout; supersedes nothing):

- **A fourth, test-only package.** A module that owns tables may have
  `internal/<module>/<module>test`, with raw-SQL fixtures for its own
  tables. Today these are `identitytest` (account), `orgtest`
  (organisation, member) and `conversationtest` (a channel with its default
  topic, and the composite organisation with owner and default channel,
  because `conversation` may import `org` and `identity`).
- **Importers.** Only `_test.go` files and higher fixture packages
  (`conversationtest` → `orgtest` → `identitytest`) import one; production
  code never does ([module import tests](../../module_imports_test.go)).
- **Imports.** A fixture package imports `kernel`, its own root and the
  roots permitted by the manifest for types, lower fixture packages, pgx
  and `testing`; never a store, a wiring package or the bridge. Another
  module's tests write a table only through its owner's fixture package.
- **Semantics.** Fixtures may build states production never creates (an
  `event_seq` without its event row, no setup row), and never add rows the
  test did not ask for, such as memberships. Scenario-specific SQL
  (adversarial, invalid or historical states) stays in the test.

**Why:** one copy of each table's insert, kept where the table is owned; Go
cannot share `_test.go` helpers between packages.

**Considered:** local raw-SQL copies in each test package (about five more
copies and 195 lines, against F14); one shared test-support package outside
the modules (a cross-module layer decision 26 avoids); building state
through the modules' wiring (it cannot create a second organisation or a
non-member, and it changes event sequences).
