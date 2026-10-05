// Package postgres implements app persistence interfaces using PostgreSQL;
// conversation's store, not this package, creates setup's default channel.
// Connections, migrations and test databases are in internal/platform/postgres.
// The event-writing flows append realtime's events through the EventAppender
// they are given, in their own transactions.
// Posting and branching take org's event_seq through the EventSequenceIn
// they are given (implemented by orgpg.SequenceIn), on their transaction.
// MessageReader is given both author lookups and the page cursor as
// snapshot-bound factories, until conversation owns the page snapshot:
// conversation's AccountDirectoryIn and MemberDirectoryIn (implemented by
// identitypg.AccountsIn and orgpg.MembersIn) and conversation's
// EventCursorIn (implemented by orgpg.EventCursorIn), each adapted by a
// closure.
// Posting and branching encode their events with conversation's codecs.
package postgres
