// Package conversationpg wires conversation to PostgreSQL (decision 26).
// Only composition roots (cmd/*) and tests import it; it holds no business
// logic. NewChannels binds channel lookups to the pool and capped creation
// to its transaction; NewTopics binds the topic lookup to the pool.
// DefaultChannelCreatorIn binds setup's default-channel write to org's transaction. NewPosting and
// NewBrancher bind their writes to transactions over the pool. Channel creation,
// posting, branching and pinning detach COMMIT and ROLLBACK from request
// cancellation with a five-second limit; their work still follows the request.
// Posting, branching and pinning raise the hub only on a confirmed commit;
// channel creation raises nothing. NewReader
// binds conversation's reads to snapshots over the pool. Only page counts and
// sidebar recounts get a local one-minute statement budget, unless the URL sets
// statement_timeout. Bounded reads and seed's count-free pages keep the session
// budget. Their runners and
// own-module bindings are unexported; EventKinds registers the event routers
// with realtime alongside PinEventKinds for pin invalidations. MessageSequencesIn binds channel bounds and message existence to
// its caller's transaction; TopicReadCandidatesIn binds topic unread bounds
// to that transaction. ChannelUnreadIn binds capped channel counts to the
// caller's snapshot; TopicUnreadIn binds topic counts and selected first unread.
package conversationpg
