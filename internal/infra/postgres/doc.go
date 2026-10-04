// Package postgres implements app and org persistence interfaces using
// PostgreSQL.
// Connections, migrations and test databases are in internal/platform/postgres.
// The event-writing flows append realtime's events through the EventAppender
// they are given, in their own transactions. Setup and sign-up are org's use
// cases; DefaultChannelCreatorIn creates setup's default channel on org's
// transaction until conversation does in step 4.
// Posting and branching take org's event_seq through the EventSequenceIn
// they are given (implemented by orgpg.SequenceIn), on their transaction.
// MessageReader is given both author lookups and the page cursor as
// snapshot-bound factories, until conversation owns the page snapshot:
// identity's accounts, MemberDirectoryIn (implemented by orgpg.MembersIn) and
// EventCursorIn (implemented by orgpg.EventCursorIn).
// EventKinds, in realtime_adapters.go, lists conversation's kinds for
// realtime's reader until conversation registers its own in step 4.
package postgres
