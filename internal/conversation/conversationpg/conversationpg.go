package conversationpg

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// EventKinds returns conversation's routers for registration with realtime's
// reader.
func EventKinds() realtime.Kinds {
	return realtime.Kinds{
		conversation.KindPosted:        conversation.RoutePosted,
		conversation.KindMessagesMoved: conversation.RouteMoved,
	}
}

// NewChannels builds the channel use cases on conversation's pool-bound store.
func NewChannels(pool *pgxpool.Pool) *conversation.Channels {
	return conversation.NewChannels(postgres.NewChannelStore(pool))
}

// NewTopics builds the topic lookups on conversation's pool-bound store.
func NewTopics(pool *pgxpool.Pool) *conversation.Topics {
	return conversation.NewTopics(postgres.NewTopicStore(pool))
}

// NewPosting builds posting with org's sequence and realtime's appender
// factories bound to its transaction. A nil notifier disables notifications.
func NewPosting(pool *pgxpool.Pool, sequences conversation.EventSequenceIn, events conversation.EventAppenderIn, notifier conversation.Notifier) *conversation.Posting {
	return conversation.NewPosting(newTxRunner(pool), writerIn, sequences, events, notifier)
}

// NewBrancher builds branching with org's sequence, realtime's appender and
// read-state factories bound to its transaction. A nil notifier disables notifications.
func NewBrancher(pool *pgxpool.Pool, sequences conversation.EventSequenceIn, events conversation.EventAppenderIn, reads conversation.ReadRangeWriterIn, notifier conversation.Notifier) *conversation.Brancher {
	return conversation.NewBrancher(newTxRunner(pool), writerIn, sequences, events, reads, notifier)
}

// NewReader builds the page, single-message and batch use case over the pool.
func NewReader(pool *pgxpool.Pool, members conversation.MemberDirectoryIn, accounts conversation.AccountDirectoryIn, cursor conversation.EventCursorIn) *conversation.Reader {
	return conversation.NewReader(newSnapshotRunner(pool), readStoreIn, members, accounts, cursor)
}

// DefaultChannelCreatorIn returns the default-channel creator bound to setup's
// transaction. Composition roots adapt it to org.DefaultChannelCreatorIn with
// a named function (decision 26).
func DefaultChannelCreatorIn(tx platform.Tx) *postgres.DefaultChannelCreator {
	return postgres.DefaultChannelCreatorIn(tx)
}

// newTxRunner returns the transaction runner posting and branching own their
// transaction through, over pool.
func newTxRunner(pool *pgxpool.Pool) conversation.TxRunner { return txRunner{pool: pool} }

type txRunner struct{ pool *pgxpool.Pool }

func (r txRunner) InTx(ctx context.Context, fn func(platform.Tx) error) error {
	return platform.InTx(ctx, r.pool, fn)
}

// writerIn returns posting's and branching's writes bound to the caller's
// transaction; it is a conversation.WriterIn.
func writerIn(tx platform.Tx) conversation.Writer { return postgres.WriterIn(tx) }

// newSnapshotRunner returns the snapshot runner the page snapshot, One and
// Many own their read through, over pool.
func newSnapshotRunner(pool *pgxpool.Pool) conversation.SnapshotRunner {
	return snapshotRunner{pool: pool}
}

type snapshotRunner struct{ pool *pgxpool.Pool }

func (r snapshotRunner) InSnapshot(ctx context.Context, fn func(platform.Snapshot) error) error {
	return platform.InSnapshot(ctx, r.pool, fn)
}

// readStoreIn returns conversation's reads bound to the caller's snapshot;
// it is a conversation.ReadStoreIn.
func readStoreIn(snapshot platform.Snapshot) conversation.ReadStore {
	return postgres.ReadStoreIn(snapshot)
}

// MessageSequencesIn binds conversation's channel bounds to the caller's transaction.
func MessageSequencesIn(tx platform.Tx) conversation.MessageSequences {
	return postgres.MessageSequencesIn(tx)
}

// TopicReadCandidatesIn binds topic unread bounds to the caller's transaction.
func TopicReadCandidatesIn(tx platform.Tx) conversation.TopicReadCandidates {
	return postgres.TopicReadCandidatesIn(tx)
}

// ChannelUnreadIn binds capped channel counts to the caller's snapshot.
func ChannelUnreadIn(s platform.Snapshot) conversation.ChannelUnread {
	return postgres.ChannelUnreadIn(s)
}
