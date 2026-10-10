// Package unread owns channel_read, read_range and topic_read_floor
// (decision 32). Read ranges name sequences so moving messages cannot change
// read state. A per-member, per-channel lock serialises unions; a join prefix
// makes pre-membership messages read. Feed and topic reads use injected transaction-bound
// conversation and org queries; this module never reads message directly.
// Reading owns feed and topic transactions through an injected runner.
// Topic preparation and batch unions retain the channel lock in the caller's
// transaction; topic floors only rise, including when no new range is added.
// Sidebar range reads share the caller's snapshot and derive bounded gaps
// from at most 101 ranges per channel, retaining the join prefix without rows.
// Channel counts deduplicate sidebar IDs and call conversation through an
// injected counter in that snapshot, using two statements for the whole list.
// Topic state loads the prefix, all ranges above it and requested floors in
// one snapshot statement, without multiplying ranges by topics. Topic counts
// combine it with an injected message counter in the same snapshot.
package unread
