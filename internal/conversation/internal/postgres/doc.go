// Package postgres is conversation's store (decisions 26 and 27): channel
// reads and creation, including the default topic, through db/queries/conversation
// on its own sqlc entry. It accepts a pool or caller-owned transaction or snapshot
// without managing its lifecycle. conversationpg.NewChannels binds it to the
// pool for production channel use cases; infra's page snapshot and setup keep
// the frozen legacy store until their migration steps.
package postgres
