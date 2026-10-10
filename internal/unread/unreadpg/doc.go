// Package unreadpg binds unread's store to caller-owned transactions and snapshots,
// wires the feed with injected message and cursor factories, and wires reading
// with a pool-backed runner and transaction-bound writers
// (decision 26). Only composition roots and tests import it.
package unreadpg
