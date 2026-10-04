// Package postgres is conversation's store (decisions 26 and 27): channel
// reads and creation, including the default topic, through db/queries/conversation
// on its own sqlc entry. It accepts a pool or caller-owned transaction or snapshot
// without managing its lifecycle. conversationpg.NewChannels binds it to the
// pool for production channel use cases, and DefaultChannelCreatorIn to
// setup's transaction for its default channel; infra's page snapshot keeps
// the frozen legacy store until its migration step.
package postgres
