// Package conversationpg wires conversation to PostgreSQL (decision 26).
// Only composition roots (cmd/*) and tests import it; it holds no business
// logic. NewChannels binds the channel use cases and NewTopics the topic
// lookup to the module's store on a pool; DefaultChannelCreatorIn binds
// setup's default-channel write to org's transaction; NewTxRunner runs
// posting's and branching's transaction over the pool and WriterIn binds
// their writes to it; NewPosting and NewBrancher build Posting and Brancher
// over them; NewReader builds Reader; NewSnapshotRunner runs its snapshot
// over the pool
// and ReadStoreIn binds conversation's reads to it; EventKinds registers its
// event routers with realtime. Branching's CreateTopic and MoveMessages run on
// copies of the legacy queries until step 4.16.
package conversationpg
