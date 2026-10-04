package org

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// DefaultChannelCreator creates an organisation's default channel, with its
// default topic, in the caller's transaction, so that a completed setup never
// lacks one. The caller owns commit and rollback, including after an error.
type DefaultChannelCreator interface {
	CreateDefaultChannel(ctx context.Context, organizationID kernel.ID) error
}

// DefaultChannelCreatorIn binds a default-channel creator to the caller's
// transaction.
type DefaultChannelCreatorIn func(platform.Tx) DefaultChannelCreator
