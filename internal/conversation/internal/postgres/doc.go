// Package postgres is conversation's store (decisions 26 and 27): channel
// reads and creation, including the default topic, the topic lookup,
// posting's and branching's writes and the page snapshot's reads, in
// db/queries/conversation on their own sqlc entry. Only conversationpg and
// this package's tests import it. It accepts a pool, or unwraps a
// caller-owned transaction or snapshot through pgxbridge, and never opens,
// commits or rolls one back.
//
// conversationpg.NewChannels and NewTopics bind the channel and topic stores
// to the pool, and DefaultChannelCreatorIn binds setup's default channel to
// org's transaction. Writer holds posting's and branching's writes and maps
// their constraints to conversation's and org's errors, except the branch
// notice's; WriterIn binds it to their transaction. ReadStore
// holds the page snapshot's channel, topic and message reads and no write,
// delegating the channel lookups and embedding the read-only TopicStore for
// the topic by ID; ReadStoreIn binds it to Reader's snapshot.
package postgres
