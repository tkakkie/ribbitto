package org

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// EventAppender appends org's encoded events in the caller's transaction.
type EventAppender interface {
	Append(ctx context.Context, organizationID kernel.ID, seq int64, kind realtime.EventKind, audience *kernel.ID, payload []byte) error
}

// EventAppenderIn binds an event appender to org's transaction.
type EventAppenderIn func(platform.Tx) EventAppender
