package postgres

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/domain"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// EventAppender is what posting and branching need from realtime's event
// log inside their transaction: an append of a payload their publisher
// encoded. infra declares it as their consumer until conversation moves in
// step 4; wiring injects realtime's appender (decision 26).
type EventAppender interface {
	Append(ctx context.Context, organizationID domain.ID, seq int64, kind realtime.EventKind, audience *domain.ID, payload []byte) error
}

// EventAppenderIn binds an EventAppender to a flow's transaction.
type EventAppenderIn func(platform.Tx) EventAppender
