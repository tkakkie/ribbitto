// Package postgres implements app and org persistence interfaces using
// PostgreSQL.
// Connections, migrations and test databases are in internal/platform/postgres.
// The event-writing flows append realtime's events through the EventAppender
// they are given, in their own transactions.
// MessageReader is given both author lookups as snapshot-bound factories,
// identity's accounts and its own MemberDirectoryIn (implemented by
// orgpg.MembersIn), until conversation owns the page snapshot.
// EventKinds, in realtime_adapters.go, lists conversation's kinds for
// realtime's reader until conversation registers its own in step 4.
package postgres
