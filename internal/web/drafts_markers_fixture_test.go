package web

import (
	"context"
	"sync"
	"testing"

	"github.com/tkakkie/ribbitto/internal/realtime"
)

type draftMarkerStream struct {
	id     int
	ctx    context.Context
	frames chan realtime.Outgoing
}

// Like unread.Changes watches, every connection gets the invalidation. A
// closing tab must not consume the only copy intended for another tab.
type draftMarkerStreams struct {
	mu         sync.Mutex
	nextStream int
	live       map[*draftMarkerStream]struct{}
}

func (s *draftMarkerStreams) subscribe(ctx context.Context) (*draftMarkerStream, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextStream++
	stream := &draftMarkerStream{id: s.nextStream, ctx: ctx, frames: make(chan realtime.Outgoing, 1)}
	if s.live == nil {
		s.live = make(map[*draftMarkerStream]struct{})
	}
	s.live[stream] = struct{}{}
	return stream, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.live, stream)
	}
}

func (s *draftMarkerStreams) publish(out realtime.Outgoing) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for stream := range s.live {
		if stream.ctx.Err() != nil {
			continue
		}
		// These are complete sidebar snapshots. Coalesce an unread snapshot
		// just as production coalesces invalidations, without blocking on a
		// client whose disconnect has not reached its request context yet.
		select {
		case <-stream.frames:
		default:
		}
		stream.frames <- out
		n++
	}
	return n
}

func TestDraftMarkerStreams(t *testing.T) {
	for _, ended := range []string{"cancelled", "released", "not-yet-cancelled"} {
		t.Run(ended, func(t *testing.T) {
			var streams draftMarkerStreams
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			old, release := streams.subscribe(ctx)
			t.Cleanup(release)
			switch ended {
			case "cancelled":
				cancel()
			case "released":
				release()
			}
			a, releaseA := streams.subscribe(t.Context())
			t.Cleanup(releaseA)
			b, releaseB := streams.subscribe(t.Context())
			t.Cleanup(releaseB)
			for _, name := range []string{"previous", "sidebar"} {
				streams.publish(realtime.Outgoing{Name: name})
			}
			for _, stream := range []*draftMarkerStream{a, b} {
				select {
				case out := <-stream.frames:
					if out.Name != "sidebar" {
						t.Fatalf("connection %d: got %q", stream.id, out.Name)
					}
				default:
					t.Fatalf("connection %d: missing broadcast", stream.id)
				}
			}
			if ended == "not-yet-cancelled" {
				select {
				case out := <-old.frames:
					if out.Name != "sidebar" {
						t.Fatalf("old connection %d: got %q", old.id, out.Name)
					}
				default:
					t.Fatalf("old connection %d: missing broadcast", old.id)
				}
			} else if len(old.frames) != 0 {
				t.Fatal("published to an ended connection")
			}
		})
	}
}
