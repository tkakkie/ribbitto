// Package pgtest provides isolated PostgreSQL databases for integration
// tests: New clones the migrated template, NewEmpty is unmigrated, and
// NewMigrator moves a database between versions for migration tests. Feature
// fixtures live with their module's tests; the shared feature fixtures stay
// in internal/infra/postgres/pgtest until migration step 5.
package pgtest
