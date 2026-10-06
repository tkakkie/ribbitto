// Package pgtest provides isolated PostgreSQL databases for integration
// tests: New clones the migrated template, NewEmpty is unmigrated, and
// NewMigrator moves a database between versions for migration tests.
// Fixtures live with the module that owns their tables (identitytest,
// orgtest, conversationtest; decision 29), not here.
package pgtest
