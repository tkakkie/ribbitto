// Package postgres is the shared PostgreSQL platform of the modules
// (decision 26): it opens the pool and the migration connection, runs the
// embedded migrations it owns, and counts statements for development
// metrics. It holds no feature queries and no query helpers; transaction and
// snapshot capabilities arrive in step 0b of the migration.
package postgres
