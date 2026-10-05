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
// unused in production until steps 4.9 and 4.10a. Branching's CreateTopic and
// MoveMessages queries duplicate the legacy entry's db/queries/topic.sql
// until step 4.16 removes that copy. ReadStore holds the page snapshot's
// channel, topic and message reads and no write, delegating the channel
// lookups and the topic by ID to the channel and topic stores;
// conversationpg.ReadStoreIn binds it to a caller's snapshot, unused in
// production until step 4.11c. Its own queries duplicate the legacy ones of
// the same names: message.sql's until step 4.15, topic.sql's ListTopics and
// LookupTopics until 4.16.
package postgres
