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

// WriterIn returns posting's writes bound to the caller's transaction; it is
// a conversation.WriterIn.
func WriterIn(tx platform.Tx) conversation.Writer { return postgres.WriterIn(tx) }
