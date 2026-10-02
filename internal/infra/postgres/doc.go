// Package postgres implements app persistence interfaces using PostgreSQL.
// It owns database connections and embedded schema migrations.
// EventLog writes realtime's durable events in caller-owned transactions.
// EventReader reads realtime's durable event log for the delivery loop and
// the watermark check (CommittedSequences); EventCleaner expires it for
// retention.
package postgres
