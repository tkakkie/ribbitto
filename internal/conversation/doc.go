// Package conversation is the conversation module's root (decisions 26 and
// 27): channels, topics, branching, posting, history and the page snapshot.
// IDs are kernel.ID.
//
// Module: conversation (feature map in docs/architecture/features.md), which
// owns the channel, topic and message tables and publishes message.posted
// and messages.moved.
//
// Posting and branching own their transactions so content and events commit
// together, then notify. History and author lookups share a snapshot so a
// page cannot mix states; the member lookup returns org's directory entry,
// not a copy. The topic lookup is scoped by a resolved membership so web
// cannot supply the organisation. The store also implements setup's injected
// default-channel write.
package conversation
