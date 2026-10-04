// Package postgres is org's store (decision 26): organization's sequences,
// replay boundary and retention lock, memberships, home slug, handles and the
// snapshot-bound member directory, in db/queries/org on their own sqlc
// entry. Only orgpg and this package's tests import it. It unwraps the
// platform's handles through pgxbridge for caller-owned snapshots and
// transactions; the authorizer and handle changer use the pool.
package postgres
