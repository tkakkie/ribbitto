// Package conversationpg wires conversation to PostgreSQL (decision 26).
// Only composition roots (cmd/*) and tests import it; it holds no business
// logic. NewChannels binds the channel use cases and NewTopics the topic
// lookup to the module's store on a pool; EventKinds registers its event
// routers with realtime.
package conversationpg
