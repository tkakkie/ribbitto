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
	return conversation.NewPosting(NewTxRunner(pool), WriterIn, sequences, events, notifier)
}

// NewBrancher builds branching with org's sequence and realtime's appender
// factories bound to its transaction. A nil notifier disables notifications.
func NewBrancher(pool *pgxpool.Pool, sequences conversation.EventSequenceIn, events conversation.EventAppenderIn, notifier conversation.Notifier) *conversation.Brancher {
	return conversation.NewBrancher(NewTxRunner(pool), WriterIn, sequences, events, notifier)
}

// NewReader builds the page, single-message and batch use case over the pool.
func NewReader(pool *pgxpool.Pool, members conversation.MemberDirectoryIn, accounts conversation.AccountDirectoryIn, cursor conversation.EventCursorIn) *conversation.Reader {
	return conversation.NewReader(NewSnapshotRunner(pool), ReadStoreIn, members, accounts, cursor)
}

// DefaultChannelCreatorIn returns the default-channel creator bound to setup's
// transaction. Composition roots adapt it to org.DefaultChannelCreatorIn with
// a closure (decision 26).
func DefaultChannelCreatorIn(tx platform.Tx) *postgres.DefaultChannelCreator {
	return postgres.DefaultChannelCreatorIn(tx)
}

// NewTxRunner returns the transaction runner posting and branching own their
// transaction through, over pool.
func NewTxRunner(pool *pgxpool.Pool) conversation.TxRunner { return txRunner{pool: pool} }

type txRunner struct{ pool *pgxpool.Pool }

func (r txRunner) InTx(ctx context.Context, fn func(platform.Tx) error) error {
	return platform.InTx(ctx, r.pool, fn)
}

// WriterIn returns posting's and branching's writes bound to the caller's
// transaction; it is a conversation.WriterIn.
func WriterIn(tx platform.Tx) conversation.Writer { return postgres.WriterIn(tx) }

// NewSnapshotRunner returns the snapshot runner the page snapshot, One and
// Many own their read through, over pool.
func NewSnapshotRunner(pool *pgxpool.Pool) conversation.SnapshotRunner {
	return snapshotRunner{pool: pool}
}

type snapshotRunner struct{ pool *pgxpool.Pool }

func (r snapshotRunner) InSnapshot(ctx context.Context, fn func(platform.Snapshot) error) error {
	return platform.InSnapshot(ctx, r.pool, fn)
}

// ReadStoreIn returns conversation's reads bound to the caller's snapshot;
// it is a conversation.ReadStoreIn.
func ReadStoreIn(snapshot platform.Snapshot) conversation.ReadStore {
	return postgres.ReadStoreIn(snapshot)
}
