// Package postgres is unread's store. It binds only to a caller-owned
// transaction or read-only snapshot and never completes either boundary. Lock-row creation uses
// a savepoint to recover a concurrent unique conflict without aborting the
// caller's writes; range replacement is protected by the channel_read lock.
// Feed coverage subtracts stored neighbours after inserting the join prefix.
// Preparation loads the read set and topic floor after establishing the join
// prefix. Batch writes coalesce additions and unions in Go and visit neighbours
// through primary-key predecessors and upper bounds; they raise the floor in
// the same locked transaction. Snapshot reads load the sidebar's first ranges
// in one statement regardless of channel count. Topic state unions channel ranges
// with requested floors in one snapshot statement. Optional capture reads raw
// ranges without a cap and stored floors only. No query reads message rows.
package postgres
