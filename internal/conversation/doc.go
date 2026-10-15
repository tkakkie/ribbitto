// Package conversation is the conversation module's root (decisions 26 and
// 27): channels, topics, branching, posting, history and page/sidebar snapshots.
// IDs are kernel.ID.
//
// Module: conversation (feature map in docs/architecture/features.md), which
// owns the channel, topic and message tables and publishes message.posted
// plus messages.moved and pins.changed.
// Message pin metadata is paired and organisation-scoped; transaction-bound
// pin writes and bounded snapshot reads retain no historical body.
// Channel.image holds a nullable illustration key; web resolves missing or
// unknown keys to a stable image from the channel ID without a backfill.
// Members choose validated bundled keys or restore NULL; image writes emit no event.
//
// Channel creation owns a transaction lock on the default channel and
// guarded channel/default-topic insert enforcing the configured count cap.
// Posting and branching own their transactions so content and events commit
// together with page-post and branch-notice read state, then notify. History and
// author lookups and injected unread counts share a snapshot so a page cannot mix states; the bounded channel-members page reads its
// channel check, listing and display names in one snapshot. Independent sidebar
// snapshots, which also serve the channel list page, read fresh bounded lists,
// counts and their event sequence without history or directories.
// Member lookups return org's directory entries, not copies. The topic lookup
// is scoped by a resolved
// membership so web cannot supply the organisation. The store also implements setup's injected
// default-channel write and transaction-bound channel message and topic unread
// range bounds and batched message existence for unread, plus snapshot-bound
// capped channel and topic counts. Read sets
// cross as values, never foreign-table reads.
package conversation
