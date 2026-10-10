// Package postgres is unread's store. It binds only to a caller-owned
// transaction and never completes that transaction. Lock-row creation uses
// a savepoint to recover a concurrent unique conflict without aborting the
// caller's writes; range replacement is protected by the channel_read lock.
// Preparation loads the read set and topic floor after establishing the join
// prefix. Batch writes coalesce additions and unions in Go and visit neighbours
// through primary-key predecessors and upper bounds; they raise the floor in
// the same locked transaction. No query reads message rows.
package postgres
