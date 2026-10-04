package conversationpg

import (
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

// DefaultChannelCreatorIn returns the default-channel creator bound to setup's
// transaction. Composition roots adapt it to org.DefaultChannelCreatorIn with
// a closure (decision 26).
func DefaultChannelCreatorIn(tx platform.Tx) *postgres.DefaultChannelCreator {
	return postgres.DefaultChannelCreatorIn(tx)
}
