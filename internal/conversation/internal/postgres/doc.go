// Package postgres is conversation's store (decisions 26 and 27): channel
// reads and creation, including the default topic, and the topic lookup,
// through db/queries/conversation on its own sqlc entry. It accepts a pool or
// caller-owned transaction or snapshot without managing its lifecycle.
// conversationpg.NewTopics binds the topic lookup to the pool; the channel
// queries are used only by this package's tests until step 4.5b.
package postgres
