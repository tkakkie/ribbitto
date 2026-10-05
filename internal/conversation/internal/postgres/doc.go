// Package postgres is conversation's store (decisions 26 and 27): channel
// reads and creation, including the default topic, and the topic lookup,
// through db/queries/conversation on its own sqlc entry. It accepts a pool or
// caller-owned transaction or snapshot without managing its lifecycle.
// conversationpg.NewChannels and NewTopics bind it to the pool for
// production, and DefaultChannelCreatorIn to setup's transaction for its
// default channel; infra's page snapshot keeps the frozen legacy store until
// its migration step. Writer holds posting's and branching's writes and maps
// their constraints to conversation's and org's errors, except the branch
// notice's; conversationpg.WriterIn binds it to a caller's transaction,
// used by posting and, from step 4.10a, branching. Branching's CreateTopic and
// MoveMessages queries duplicate the legacy entry's db/queries/topic.sql
// until step 4.16 removes that copy. ReadStore holds the page snapshot's
// message reads and no write; conversationpg.ReadStoreIn binds it to a
// caller's snapshot, unused in production until step 4.11c. Its queries
// duplicate message.sql's legacy ones of the same names, which step 4.15
// removes.
package postgres
