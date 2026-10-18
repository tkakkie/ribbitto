// Package postgres is the shared PostgreSQL platform of the modules
// (decision 26): it opens the pool with overridable server-side timeouts and
// the migration connection without those defaults, runs the
// embedded migrations it owns under a session lock, checks schema compatibility
// at server startup, and counts statements for development
// metrics. It also opens the opaque Tx and Snapshot that use cases pass to
// the modules they orchestrate; only stores unwrap them, through pgxbridge.
// Longer snapshot budgets apply only to its own default statement_timeout;
// explicit URL values, including startup options, always win.
// It holds no feature queries and no query helpers.
package postgres
