package conversationpg

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
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
