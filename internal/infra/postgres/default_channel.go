package postgres

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/domain"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// DefaultChannelCreator implements org.DefaultChannelCreator over the channel
// store, standing in for conversation until step 4.
type DefaultChannelCreator struct{ channels *ChannelStore }

// DefaultChannelCreatorIn binds a DefaultChannelCreator to tx without
// managing its lifecycle.
func DefaultChannelCreatorIn(tx platform.Tx) *DefaultChannelCreator {
	return &DefaultChannelCreator{channels: NewChannelStore(pgxbridge.Tx(tx))}
}

// CreateDefaultChannel creates the organisation's default channel, named
// conversation.DefaultChannelName; the channel store creates its default topic with it.
func (c *DefaultChannelCreator) CreateDefaultChannel(ctx context.Context, organizationID domain.ID) error {
	_, err := c.channels.CreateChannel(ctx, organizationID, conversation.DefaultChannelName, true)
	return err
}
