// Package unread owns channel_read, read_range and topic_read_floor
// (decision 32). Read ranges name sequences so moving messages cannot change
// read state. A per-member, per-channel lock serialises unions; a join prefix
// makes pre-membership messages read. Feed and topic reads use injected transaction-bound
// conversation and org queries; this module never reads message directly.
// Feed and topic writers report actual newly read messages under that lock,
// excluding implicit prefixes, empty coverage and floor-only writes. Reading
// owns their transactions and synchronously calls a context-free publisher only
// after a successful commit with changed=true, or when the commit's outcome is
// unknown (a recount is right either way); never after a rollback or an error reply.
// Production shares one Changes registry with sidebar streams when the hub is wired;
// without a hub the publisher is nil. Committed reads survive request cancellation.
// Posting and branching use only transaction-bound writers
// and their durable message.posted notification, never the reading publisher.
// Topic preparation and batch unions retain the channel lock in the caller's
// transaction; topic floors only rise, including when no new range is added.
// Sidebar range reads share the caller's snapshot and derive bounded gaps
// from at most 101 ranges per channel, retaining the join prefix without rows.
// Channel counts deduplicate sidebar IDs and call conversation through an
// injected counter in that snapshot, using two statements for the whole list.
// Topic state loads the prefix, all ranges above it and requested floors in
// one snapshot statement, without multiplying ranges by topics. Topic counts
// combine it with an injected message counter in the same snapshot.
// Process-local member change generations share wake channels only among active
// watches of one organisation/member; releasing the last watch removes its state.
// Persisted read state remains authoritative across subscription lifetimes.
package unread
