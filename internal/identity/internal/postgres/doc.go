// Package postgres is identity's store (decision 26): the account and session
// queries in db/queries/identity, on their own sqlc entry. Only identitypg and
// this package's tests import it. It unwraps the platform's handles through
// pgxbridge: a Snapshot for the directory read inside a caller's snapshot,
// and a Tx for ReplaceSession's delete and insert and AccountCreator's insert
// in a caller-owned transaction. AccountCreator never manages its lifecycle.
package postgres
