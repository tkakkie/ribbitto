package realtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestHubGenerationCompetingRaises(t *testing.T) {
	for _, kind := range []Interest{InterestPresence, InterestTyping} {
		for _, delayed := range []int64{1, 3} {
			t.Run(fmt.Sprintf("%s/%d", kind, delayed), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					h := NewHub()
					read, resume := make(chan struct{}), make(chan struct{})
					gate := sync.OnceFunc(func() { close(read); <-resume })
					h.beforeRaise = func(level int64) {
						if level == delayed {
							gate()
						}
					}
					entry := h.sequence(orgA)
					level := &entry.presence
					if kind == InterestTyping {
						level = &entry.typing
					}
					old := level.Load()
					done := make(chan struct{})
					go func() { h.RaiseGeneration(orgA, kind, delayed); close(done) }()
					<-read
					h.RaiseGeneration(orgA, kind, 2)
					close(resume)
					<-done
					select {
					case <-old.changed:
					default:
						t.Error("publication did not close the old channel")
					}
					presence, typing := entry.presence.Load(), entry.typing.Load()
					if max(presence.latest, typing.latest) != max(2, delayed) || h.Latest(orgA) != 0 {
						t.Error("competing generation raises regressed or changed the durable level")
					}
					h.RaiseGeneration(orgA, kind, 0)
					h.RaiseGeneration(orgA, kind, max(2, delayed))
					if entry.presence.Load() != presence || entry.typing.Load() != typing {
						t.Error("a raise changed another kind or lowered a generation")
					}
					select {
					case <-level.Load().changed:
						t.Error("an equal or lower raise closed the current channel")
					default:
					}
				})
			})
		}
	}
}

// Each negative fixture differs from matching in exactly one predicate.
// The read-to-wait gate proves that generation channels are captured before
// blocking; synctest then proves a negative raise leaves the writer blocked.
func TestStreamGenerationWake(t *testing.T) {
	for _, kind := range []Interest{InterestPresence, InterestTyping} {
		other := InterestPresence
		if kind == InterestPresence {
			other = InterestTyping
		}
		for _, tt := range []struct {
			name     string
			org      kernel.ID
			interest Interest
			reads    int
		}{
			{"matching", orgA, kind, 2},
			{"organisation", orgB, kind, 1},
			{"interest", orgA, other, 1},
		} {
			t.Run(string(kind)+"/"+tt.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					h := NewHub()
					read, resume := make(chan struct{}), make(chan struct{})
					h.afterWaitRead = sync.OnceFunc(func() { close(read); <-resume })
					selected := sub
					selected.Interests = []Interest{tt.interest}
					log, send := &fakeLog{maxReads: 3}, newRecorder()
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					done := make(chan result, 1)
					go func() {
						cursor, err := (Stream{Hub: h, Events: log}).Run(ctx, selected, 0, send)
						done <- result{cursor, err}
					}()
					<-read
					h.RaiseGeneration(tt.org, kind, 1)
					close(resume)
					synctest.Wait()
					if got := log.readCount(); got != tt.reads {
						t.Errorf("reads = %d, want %d after generation raise", got, tt.reads)
					}
					if h.Waiting(orgA) != 1 || len(send.ids()) != 0 || send.heartbeatCount() != 0 {
						t.Error("writer spun, sent a frame, or failed to return to waiting")
					}
					cancel()
					got := <-done
					if got.cursor != 0 || !errors.Is(got.err, context.Canceled) || h.Waiting(orgA) != 0 {
						t.Errorf("Run = %+v; waiters %d, want cursor 0, cancellation and no waiters", got, h.Waiting(orgA))
					}
				})
			})
		}
	}
}
