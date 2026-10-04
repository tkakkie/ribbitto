// Package postgres implements app persistence interfaces using PostgreSQL.
// Connections, migrations and test databases are in internal/platform/postgres.
// The event-writing flows append realtime's events through the EventAppender
// they are given, in their own transactions.
// MessageReader is given both author lookups as snapshot-bound factories,
// identity's accounts and its own MemberDirectoryIn (implemented by
// orgpg.MembersIn), until conversation owns the page snapshot.
// EventKinds, in realtime_adapters.go, lists the publishers' kinds for
// realtime's reader until each module registers its own.
package postgres
