// Package postgres is conversation's store (decisions 26 and 27): channel
// reads and creation, including the default topic, and the topic lookup,
// through db/queries/conversation on its own sqlc entry. It accepts a pool or
// caller-owned transaction or snapshot without managing its lifecycle.
// conversationpg.NewChannels and NewTopics bind it to the pool for
// production, and DefaultChannelCreatorIn to setup's transaction for its
// default channel; infra's page snapshot keeps the frozen legacy store until
// its migration step. Writer holds posting's writes and maps their
// constraints to conversation's and org's errors; conversationpg.WriterIn
// binds it to a caller's transaction, unused in production until step 4.9.
package postgres
