package realtime

import (
	"context"
	"log/slog"
	"time"
)

// DefaultRetention keeps seven days of replay history.
const DefaultRetention = 7 * 24 * time.Hour

// EventCleaner deletes expired rows and raises replay boundaries in atomic batches.
type EventCleaner interface {
	ExpireEvents(context.Context, time.Time) error
}

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
