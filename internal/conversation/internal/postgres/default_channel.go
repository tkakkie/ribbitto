package postgres

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgxbridge"
)

// DefaultChannelCreator creates setup's default channel on the caller's
// transaction; setup owns its commit and rollback.
type DefaultChannelCreator struct{ channels *ChannelStore }

// DefaultChannelCreatorIn binds a DefaultChannelCreator to tx without
// managing its lifecycle.
func DefaultChannelCreatorIn(tx platform.Tx) *DefaultChannelCreator {
	return &DefaultChannelCreator{channels: NewChannelStore(pgxbridge.Tx(tx))}
}

// CreateDefaultChannel creates the organisation's default channel, named
// conversation.DefaultChannelName; CreateChannel writes its default topic in
// the same statement.
func (c *DefaultChannelCreator) CreateDefaultChannel(ctx context.Context, organizationID kernel.ID) error {
	_, err := c.channels.CreateChannel(ctx, organizationID, conversation.DefaultChannelName, true)
	return err
}
