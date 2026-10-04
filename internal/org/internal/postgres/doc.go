// Package postgres is org's store (decision 26): organization's sequences,
// replay boundary and retention lock, in db/queries/org on their own sqlc
// entry. Only orgpg and this package's tests import it. It unwraps the
// platform's handles through pgxbridge, so it reads and writes only in the
// snapshot or transaction it is given.
package postgres
