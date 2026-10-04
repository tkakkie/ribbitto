// Package postgres is conversation's store (decisions 26 and 27): channel
// reads and creation, including the default topic, through db/queries/conversation
// on its own sqlc entry. It accepts a pool or caller-owned transaction or snapshot
// without managing its lifecycle. Only this package's tests use it until step
// 4.5b adds conversationpg's bindings; production still uses the legacy store.
package postgres
