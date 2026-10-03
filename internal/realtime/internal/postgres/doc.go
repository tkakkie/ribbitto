// Package postgres is realtime's store (decision 26): the event log's reads
// and appends in db/queries/realtime, on their own sqlc entry. Only realtimepg and this
// package's tests import it. It never reads organization: the cursor bounds
// come through an injected realtime.Bounds, read in the same snapshot as the
// rows, which it unwraps through pgxbridge.
package postgres
