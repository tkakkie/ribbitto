// Package conversationpg wires conversation to PostgreSQL (decision 26).
// Only composition roots (cmd/*) and tests import it; it holds no business
// logic. NewChannels binds channel lookups to the pool and capped creation
// to its transaction; NewTopics binds the topic lookup to the pool.
// DefaultChannelCreatorIn binds setup's default-channel write to org's transaction. NewPosting and
// NewBrancher bind their writes to transactions over the pool; NewReader
// binds conversation's reads to snapshots over the pool. Their runners and
// own-module bindings are unexported; EventKinds registers the event routers
// with realtime (PinEventKinds stays separate until live activation). MessageSequencesIn binds channel bounds and message existence to
// its caller's transaction; TopicReadCandidatesIn binds topic unread bounds
// to that transaction. ChannelUnreadIn binds capped channel counts to the
// caller's snapshot; TopicUnreadIn binds topic counts and selected first unread.
package conversationpg
