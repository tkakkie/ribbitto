// Package postgres is org's store (decision 26): organization's event
// sequence and page cursor, committed sequences, replay boundary and
// retention lock, memberships, home slug, handles, the snapshot-bound member
// directory, and setup's and sign-up's registration writes and setup state,
// in db/queries/org on their own sqlc entry. Only orgpg and this package's
// tests import it. It unwraps the platform's handles through pgxbridge for
// caller-owned snapshots and transactions, and never opens, commits or rolls
// back one; the authorizer, handle changer and setup state use the pool.
package postgres
