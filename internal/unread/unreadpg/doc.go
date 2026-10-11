// Package unreadpg binds unread's store to caller-owned transactions and snapshots,
// exposes sidebar ranges and topic state as snapshot-bound readers,
// wires the feed with injected message and cursor factories, and wires reading
// with a pool-backed runner, transaction-bound writers and an optional post-commit publisher
// (decision 26). Only composition roots and tests import it.
package unreadpg
