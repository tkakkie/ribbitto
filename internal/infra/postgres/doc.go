// Package postgres implements app persistence interfaces using PostgreSQL.
// Connections, migrations and test databases are in internal/platform/postgres.
// The event-writing flows append realtime's events through the EventAppender
// they are given, in their own transactions.
// Posting and branching take org's event_seq through the EventSequenceIn
// they are given (implemented by orgpg.SequenceIn), on their transaction.
// MessageReader is given both author lookups and the page cursor as
// snapshot-bound factories: identity's accounts, its own MemberDirectoryIn
// and EventCursorIn (implemented by orgpg.MembersIn and orgpg.EventCursorIn),
// until conversation owns the page snapshot.
// EventKinds, in realtime_adapters.go, lists the publishers' kinds for
// realtime's reader until each module registers its own.
package postgres
