// Package postgres implements app persistence interfaces using PostgreSQL.
// Connections, migrations and test databases are in internal/platform/postgres.
// EventLog writes realtime's durable events in caller-owned transactions.
// EventReader reads realtime's durable event log for the delivery loop and
// the watermark check (CommittedSequences); EventCleaner expires it for
// retention.
package postgres
