// Package postgres is realtime's store (decision 26): the event log's reads,
// appends and retention in db/queries/realtime, on their own sqlc entry. Only
// realtimepg and this package's tests import it. It never reads or writes
// organization: the cursor bounds come through an injected realtime.Bounds,
// read in the same snapshot as the rows, and retention's lock and boundary
// through an injected realtime.RetentionBoundary. It unwraps the platform's
// handles through pgxbridge.
// Retention's full-log listing gets a local one-minute read budget unless the
// URL sets statement_timeout; bounded deletion batches retain session timeouts.
package postgres
