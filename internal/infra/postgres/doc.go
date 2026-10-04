// Package postgres implements app persistence interfaces using PostgreSQL.
// Connections, migrations and test databases are in internal/platform/postgres.
// The event-writing flows append realtime's events through the EventAppender
// they are given, in their own transactions.
// EventKinds, in realtime_adapters.go, lists conversation's kinds for
// realtime's reader until conversation registers its own in step 4.
package postgres
