package web

import (
	"context"
	"sync"
	"testing"

	"github.com/tkakkie/ribbitto/internal/realtime"
)

type draftMarkerFrame struct {
	id  int
	out realtime.Outgoing
}

type draftMarkerStream struct {
	id     int
	ctx    context.Context
	frames chan draftMarkerFrame
}

// Like unread.Changes watches, every connection gets the invalidation. A
// closing tab must not consume the only copy intended for another tab.
type draftMarkerStreams struct {
	mu                    sync.Mutex
	nextStream, nextFrame int
	live                  map[*draftMarkerStream]struct{}
	trace                 func(string, ...any) // DIAG(#1444)
}

func (s *draftMarkerStreams) record(stream *draftMarkerStream, frame draftMarkerFrame, action string, err error) {
	if s.trace != nil {
		s.trace("connection=%d frame=%d %s contextDone=%t error=%v", stream.id, frame.id, action, stream.ctx.Err() != nil, err)
	}
}

func (s *draftMarkerStreams) subscribe(ctx context.Context) (*draftMarkerStream, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextStream++
	stream := &draftMarkerStream{id: s.nextStream, ctx: ctx, frames: make(chan draftMarkerFrame, 1)}
	if s.live == nil {
		s.live = make(map[*draftMarkerStream]struct{})
	}
	s.live[stream] = struct{}{}
	s.record(stream, draftMarkerFrame{}, "registered", nil)
	return stream, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.live, stream)
		s.record(stream, draftMarkerFrame{}, "released", nil)
	}
}

func (s *draftMarkerStreams) publish(out realtime.Outgoing) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextFrame++
	frame := draftMarkerFrame{id: s.nextFrame, out: out}
	n := 0
	for stream := range s.live {
		if stream.ctx.Err() != nil {
			s.record(stream, frame, "excluded", nil)
			continue
		}
		// These are complete sidebar snapshots. Coalesce an unread snapshot
		// just as production coalesces invalidations, without blocking on a
		// client whose disconnect has not reached its request context yet.
		select {
		case <-stream.frames:
		default:
		}
		stream.frames <- frame
		s.record(stream, frame, "queued", nil)
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
			if ended == "cancelled" {
				cancel()
			} else if ended == "released" {
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
				case frame := <-stream.frames:
					if frame.out.Name != "sidebar" {
						t.Fatalf("connection %d: got %q", stream.id, frame.out.Name)
					}
				default:
					t.Fatalf("connection %d: missing broadcast", stream.id)
				}
			}
			if ended != "not-yet-cancelled" && len(old.frames) != 0 {
				t.Fatal("published to an ended connection")
			}
		})
	}
}
