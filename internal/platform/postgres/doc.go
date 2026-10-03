// Package postgres is the shared PostgreSQL platform of the modules
// (decision 26): it opens the pool and the migration connection, runs the
// embedded migrations it owns, and counts statements for development
// metrics. It also opens the opaque Tx and Snapshot that use cases pass to
// the modules they orchestrate; only stores unwrap them, through pgxbridge.
// It holds no feature queries and no query helpers.
package postgres
