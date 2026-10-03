package postgres

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/domain"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// EventAppender is what the event-writing flows (posting, branching, setup
// and sign-up) need from realtime's event log inside their transaction: an
// append of a payload their publisher encoded. infra declares it as their
// consumer until the flows move (org in step 3, conversation in step 4);
// wiring injects realtime's appender (decision 26).
type EventAppender interface {
	Append(ctx context.Context, organizationID domain.ID, seq int64, kind realtime.EventKind, audience *domain.ID, payload []byte) error
}

// EventAppenders binds an EventAppender to a flow's transaction.
type EventAppenders func(platform.Tx) EventAppender
