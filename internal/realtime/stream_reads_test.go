package realtime

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestStreamNonDurableWakesDoNotReadEvents(t *testing.T) {
	for _, start := range []string{"caught up", "cold backlog", "hub ahead"} {
		t.Run(start, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, w, log := NewHub(), newSidebarWatch(), &fakeLog{maxReads: 20}
				cursor, wantReads := int64(7), 1
				if start == "cold backlog" {
					log.append(posted(8, channelA), posted(9, channelA), posted(10, channelA), posted(11, channelA))
					cursor, wantReads = 11, 3 // Two full batches still need a final short read.
				}
				if start == "hub ahead" {
					h.Raise(orgA, 50)
					wantReads = 2
				}
				selected := sub
				selected.Interests = []Interest{InterestMessages, InterestSidebar, InterestPresence, InterestTyping}
				counts, ownerReads := 0, 0
				generations := map[Interest]int64{}
				stream := Stream{Hub: h, Events: log, BatchSize: 2,
					Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render),
					Sidebar: watchedSidebar{sidebarSource{sidebarTrigger, func(context.Context, Subscription) (Outgoing, error) {
						counts++
						return Outgoing{}, nil
					}}, func(kernel.ID, kernel.ID) SidebarWatch { return w }},
					Owners: map[Interest]EphemeralOwner{},
				}
				for _, kind := range []Interest{InterestPresence, InterestTyping} {
					stream.Owners[kind] = ownerFunc(func(Subscription, int64) (EphemeralFrame, error) {
						ownerReads++
						return EphemeralFrame{Organization: orgA, Channel: channelA, Generation: generations[kind], Outgoing: Outgoing{Name: string(kind)}}, nil
					})
				}
				send := newRecorder()
				send.failOn = -1
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan result, 1)
				go func() { c, err := stream.Run(ctx, selected, 7, send); done <- result{c, err} }()
				settled := func(phase string) {
					t.Helper()
					synctest.Wait()
					if log.readCount() != wantReads || h.Waiting(orgA) != 1 {
						t.Fatalf("%s: event-reader calls=%d, want %d; waiters=%d", phase, log.readCount(), wantReads, h.Waiting(orgA))
					}
				}
				settled("initial drain")
				w.publish()
				settled("watch-only wake")
				w.publish()
				settled("second watch-only wake")
				if counts != 1 {
					t.Fatalf("early recounts=%d, want 1", counts)
				}
				for _, kind := range []Interest{InterestPresence, InterestTyping} {
					generations[kind] = 1
					h.RaiseGeneration(orgA, kind, 1)
					settled(string(kind) + "-only wake")
				}
				if ownerReads != 4 {
					t.Fatalf("owner reads=%d, want 4", ownerReads)
				}
				<-time.After(SidebarInterval)
				settled("recount deadline")
				if counts != 2 {
					t.Fatalf("deadline recounts=%d, want 2", counts)
				}
				log.append(posted(cursor+1, channelA))
				h.Raise(orgA, max(cursor+1, h.Latest(orgA)+1))
				wantReads++
				settled("new durable level")
				if ids := send.ids(); !slices.Contains(ids, cursor+1) {
					t.Fatalf("durable wake lost: frames=%v, want event %d", ids, cursor+1)
				}
				cancel()
				if got := <-done; got.cursor != cursor+1 || !errors.Is(got.err, context.Canceled) || w.releases != 1 {
					t.Fatalf("Run=%+v releases=%d", got, w.releases)
				}
			})
		})
	}
}

func TestStreamDurableRaiseDuringRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, log, send := NewHub(), &fakeLog{}, newRecorder()
		captured, resume := make(chan struct{}), make(chan struct{})
		calls := 0
		reader := sidebarReader(func(ctx context.Context, org kernel.ID, after int64, limit int) ([]Event, error) {
			calls++
			events, err := log.EventsAfter(ctx, org, after, limit)
			if calls == 1 {
				close(captured)
				<-resume // Keep the empty snapshot older than the concurrent commit and raise.
			}
			return events, err
		})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := runAsync(ctx, Stream{Hub: h, Events: reader, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render)}, 0, send)
		<-captured
		log.append(posted(1, channelA))
		h.Raise(orgA, 1)
		close(resume)
		synctest.Wait()
		if calls != 2 || !slices.Equal(send.ids(), []int64{1}) || h.Waiting(orgA) != 1 {
			t.Fatalf("raise during read lost: event-reader calls=%d frames=%v waiters=%d", calls, send.ids(), h.Waiting(orgA))
		}
		cancel()
		if got := <-done; got.cursor != 1 || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("Run=%+v", got)
		}
	})
}
