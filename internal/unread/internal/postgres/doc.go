// Package postgres is unread's store. It binds only to a caller-owned
// transaction and never completes that transaction. Lock-row creation uses
// a savepoint to recover a concurrent unique conflict without aborting the
// caller's writes; range replacement is protected by the channel_read lock.
// Queries visit only the primary-key predecessor and overlapping or touching
// neighbours, never message rows or the whole read set.
package postgres
