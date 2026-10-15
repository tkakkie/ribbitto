package realtime

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

var errRecountDeferred = errors.New("realtime: recount slot wait expired")

// RecountLimit bounds computations across all sidebar triggers.
// Registration capacity is independent; a deferred computation retains its stream.
type RecountLimit struct {
	slots  chan struct{}
	random func(time.Duration) time.Duration
}

// NewRecountLimit creates a process-wide limit; capacity must be positive.
func NewRecountLimit(capacity int) *RecountLimit {
	if capacity <= 0 {
		panic("recount capacity must be positive")
	}
	return &RecountLimit{slots: make(chan struct{}, capacity), random: func(bound time.Duration) time.Duration {
		return time.Duration(rand.Int64N(int64(bound) + 1))
	}}
}

func (l *RecountLimit) connectDelay() time.Duration {
	if l == nil {
		return 0
	}
	return l.random(3 * time.Second)
}

func (l *RecountLimit) retryAt() time.Time {
	return time.Now().Add(250*time.Millisecond + l.random(750*time.Millisecond))
}

func (s Stream) computeSidebar(ctx context.Context, sub Subscription) (Outgoing, error) {
	if s.RecountLimit == nil {
		return s.Sidebar.Recount(ctx, sub)
	}
	wait, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	select {
	case s.RecountLimit.slots <- struct{}{}:
		defer func() { <-s.RecountLimit.slots }()
	case <-wait.Done():
		if ctx.Err() != nil {
			return Outgoing{}, context.Cause(ctx)
		}
		return Outgoing{}, errRecountDeferred
	}
	return s.Sidebar.Recount(ctx, sub)
}
