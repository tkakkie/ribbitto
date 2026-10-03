// Package postgres implements app persistence interfaces using PostgreSQL.
// Connections, migrations and test databases are in internal/platform/postgres.
// EventLog writes realtime's durable events in caller-owned transactions.
// EventBoundsIn and EventSequences read org's cursor bounds and committed
// sequences for realtime's reader and watermark until org's module moves
// (step 3); EventCleaner expires the event log for retention.
package postgres
