package realtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A failed cleanup is retried with the next cutoff; cancellation stops the loop.
type retentionCleanerFunc func(context.Context, time.Time) error

func (f retentionCleanerFunc) ExpireEvents(ctx context.Context, cutoff time.Time) error {
	return f(ctx, cutoff)
}

func TestRetentionTicks(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ticks, calls, done := make(chan time.Time), make(chan time.Time, 1), make(chan struct{})
	r := Retention{Period: time.Hour, Events: retentionCleanerFunc(func(ctx context.Context, cutoff time.Time) error {
		calls <- cutoff
		return errors.New("retry")
	})}
	before := time.Now().Add(-time.Hour)
	go func() { r.Run(ctx, ticks); close(done) }()
	// No tick has been sent: even a process that restarts hourly must clean up.
	if got := receive(t, calls); got.Before(before) || got.After(time.Now().Add(-time.Hour)) {
		t.Fatalf("startup cutoff = %v, want current time minus retention", got)
	}
	for _, now := range []time.Time{time.Unix(10000, 0), time.Unix(20000, 0)} {
		ticks <- now
		if got := receive(t, calls); !got.Equal(now.Add(-time.Hour)) {
			t.Fatalf("cutoff = %v", got)
		}
	}
	cancel()
	receive(t, done)
}
