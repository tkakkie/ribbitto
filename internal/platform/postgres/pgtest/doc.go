// Package pgtest provides isolated PostgreSQL databases for integration
// tests: New clones the migrated template, NewEmpty is unmigrated. Feature
// fixtures do not belong here; they live with their module's tests (and,
// until each module moves, in internal/infra/postgres/pgtest).
package pgtest
