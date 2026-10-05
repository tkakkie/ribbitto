// Package pgtest provides isolated PostgreSQL databases for integration
// tests: New clones the migrated template, NewEmpty is unmigrated. Feature
// fixtures live with their module's tests; the shared feature fixtures stay
// in internal/infra/postgres/pgtest until migration step 5.
package pgtest
