package realtime

import (
	"context"
	"log/slog"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// DefaultRetention keeps seven days of replay history.
const DefaultRetention = 7 * 24 * time.Hour

// EventCleaner deletes expired rows and raises replay boundaries in atomic batches.
type EventCleaner interface {
	ExpireEvents(context.Context, time.Time) error
}

// RetentionBoundary locks an organisation for retention and raises its
// replay boundary, inside the cleaner's transaction; org owns both (decision
// 26). Posting takes the same lock first, so the lock orders the two.
type RetentionBoundary interface {
	LockForRetention(ctx context.Context, organizationID kernel.ID) error
	// RaiseBoundary sets the boundary to through only if that is higher.
	// The boundary only rises, so a cursor whose replay is incomplete is
	// never admitted again.
	RaiseBoundary(ctx context.Context, organizationID kernel.ID, through int64) error
}

// RetentionBoundaryIn binds a RetentionBoundary to the cleaner's transaction.
type RetentionBoundaryIn func(platform.Tx) RetentionBoundary

// Retention expires replay history at start and on each tick; messages and unread state remain.
type Retention struct {
	Events EventCleaner
	// Period defaults to DefaultRetention when nonpositive.
	Period time.Duration
}

// Run cleans up immediately, retries failed cleanups on the next tick and stops with ctx.
func (r Retention) Run(ctx context.Context, ticks <-chan time.Time) {
	if r.Period <= 0 {
		r.Period = DefaultRetention
	}
	now := time.Now()
	for {
		if ctx.Err() != nil {
			return
		}
		check, cancel := context.WithTimeout(ctx, time.Minute)
		err := r.Events.ExpireEvents(check, now.Add(-r.Period))
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "event retention failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case now = <-ticks:
		}
	}
}
