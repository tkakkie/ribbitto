// Package postgres implements app persistence interfaces using PostgreSQL.
// Connections, migrations and test databases are in internal/platform/postgres.
// The event-writing flows append realtime's events through the EventAppender
// they are given, in their own transactions.
// EventBoundsIn and EventSequences read org's cursor bounds and committed
// sequences for realtime's reader and watermark, and RetentionBoundaryIn
// locks an organisation and raises its replay boundary for realtime's
// cleaner, until org's module moves (step 3).
package postgres
