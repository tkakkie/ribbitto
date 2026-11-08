package realtime

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

var errRecountDeferred = errors.New("realtime: recount slot wait expired")

// RecountLimit bounds page counts and computations across all sidebar triggers.
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

// Acquire waits at most 25 ms for shared count capacity. A nil limit admits
// without a slot. The caller must defer release until the computation returns.
func (l *RecountLimit) Acquire(ctx context.Context) (release func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}
	if l == nil {
		return func() {}, nil
	}
	wait, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	select {
	case l.slots <- struct{}{}:
		return func() { <-l.slots }, nil
	case <-wait.Done():
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		return nil, errRecountDeferred
	}
}

func (s Stream) computeSidebar(ctx context.Context, sub Subscription) (Outgoing, error) {
	release, err := s.RecountLimit.Acquire(ctx)
	if err != nil {
		return Outgoing{}, err
	}
	defer release()
	return s.Sidebar.Recount(ctx, sub)
}
