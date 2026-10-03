// Package postgres is identity's store (decision 26): the account and session
// queries in db/queries/identity, on their own sqlc entry. Only identitypg and
// this package's tests import it. It unwraps the platform's Snapshot through
// pgxbridge for the directory read inside a caller's snapshot.
package postgres
