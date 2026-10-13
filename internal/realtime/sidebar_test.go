package realtime

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

type sidebarSource struct {
	trigger func(Event) bool
	recount func(context.Context, Subscription) (Outgoing, error)
}

func (s sidebarSource) Trigger(e Event) bool { return s.trigger(e) }
func (s sidebarSource) Recount(ctx context.Context, sub Subscription) (Outgoing, error) {
	return s.recount(ctx, sub)
}

type sidebarReader func(context.Context, kernel.ID, int64, int) ([]Event, error)

func (f sidebarReader) EventsAfter(ctx context.Context, org kernel.ID, after int64, limit int) ([]Event, error) {
	return f(ctx, org, after, limit)
}

func sidebarTrigger(e Event) bool { return e.Kind == kindPosted || e.Kind == kindMoved }

// Negative fixtures differ in one predicate; the reader deliberately supplies
// a foreign envelope so its own organisation scope cannot mask the pre-filter.
func TestSidebarTriggersAndDeadline(t *testing.T) { testSidebarTriggersAndDeadline(t, false) }

func testSidebarTriggersAndDeadline(t *testing.T, scheduled bool) {
	for _, name := range []string{"burst", "moved", "channel", "topic", "sidebar only", "organisation", "kind", "interest"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, log, send := NewHub(), &fakeLog{}, newRecorder()
				send.failOn = -1
				selected, triggers := sub, 0
				var reads atomic.Int64
				selected.Interests = []Interest{InterestSidebar, InterestMessages}
				if name == "sidebar only" {
					selected.Interests = []Interest{InterestSidebar}
				}
				if name == "interest" {
					selected.Interests = nil
				}
				selected.Topic = &kernel.ID{1}
				stream := Stream{Hub: h, Renderer: rendererFunc(render), Authorizer: authorizerFunc(allowAll),
					Events: sidebarReader(func(ctx context.Context, org kernel.ID, after int64, limit int) ([]Event, error) {
						events, err := log.EventsAfter(ctx, org, after, limit)
						if name == "organisation" {
							for i := range events {
								events[i].OrganizationID = orgB
							}
						}
						return events, err
					}), Sidebar: sidebarSource{func(e Event) bool { triggers++; return sidebarTrigger(e) },
						func(_ context.Context, s Subscription) (Outgoing, error) {
							reads.Add(1)
							if s.Organization != selected.Organization || s.Account != selected.Account || s.Channel != selected.Channel {
								t.Error("recount lost the subscription scope or viewer")
							}
							return Outgoing{ID: 999, Data: []byte("full recount")}, nil
						}}}
				stream = compatibilityStream(t, stream, &selected, scheduled)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan result, 1)
				go func() { c, err := stream.Run(ctx, selected, 0, send); done <- result{c, err} }()
				synctest.Wait()
				for seq := int64(1); seq <= 5; seq++ {
					e := posted(seq, channelA)
					e.Topics = []kernel.ID{{2}}
					if name == "channel" {
						e.ChannelID = channelB
					}
					if seq%2 == 0 || name == "moved" {
						e.Kind = kindMoved
					}
					if name == "kind" {
						e.Kind = kindJoined
					}
					log.append(e)
					h.Raise(orgA, seq)
					synctest.Wait()
				}
				time.Sleep(SidebarInterval - time.Nanosecond)
				synctest.Wait()
				initial := 1
				if name == "interest" {
					initial = 0
				}
				if reads.Load() != int64(initial) {
					t.Fatalf("burst recounted before minimum interval: %d", reads.Load())
				}
				time.Sleep(time.Nanosecond) // No new events: the pending deadline must wake the writer.
				synctest.Wait()
				want := 2
				if name == "organisation" || name == "kind" {
					want = 1
				}
				if name == "interest" {
					want = 0
				}
				if reads.Load() != int64(want) || len(send.ids()) != want || (name == "organisation" && triggers != 0) {
					t.Fatalf("recounts=%d frames=%v triggers=%d; want %d", reads.Load(), send.ids(), triggers, want)
				}
				cancel()
				if got := <-done; got.cursor != 5 || !errors.Is(got.err, context.Canceled) {
					t.Fatalf("Run=%+v", got)
				}
			})
		})
	}
}

func TestSidebarReconnectAfterMessageBeforeRecount(t *testing.T) {
	testSidebarReconnectAfterMessageBeforeRecount(t, false)
}

func testSidebarReconnectAfterMessageBeforeRecount(t *testing.T, scheduled bool) {
	synctest.Test(t, func(t *testing.T) {
		h, log, reads := NewHub(), &fakeLog{}, 0
		selected := sub
		selected.Interests = []Interest{InterestMessages, InterestSidebar}
		stream := Stream{Hub: h, Events: log, Renderer: rendererFunc(render), Authorizer: authorizerFunc(allowAll),
			Sidebar: sidebarSource{sidebarTrigger, func(context.Context, Subscription) (Outgoing, error) {
				reads++
				return Outgoing{ID: 999, Data: []byte{byte(reads)}}, nil
			}}}
		stream = compatibilityStream(t, stream, &selected, scheduled)
		var frames []Outgoing
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan result, 1)
		send := frameSender{func(out Outgoing) error {
			frames = append(frames, out)
			if out.Name == "message" {
				cancel() // Disconnect after the durable write, while its recount is pending.
			}
			return nil
		}}
		go func() { c, err := stream.Run(ctx, selected, 0, send); done <- result{c, err} }()
		synctest.Wait()
		log.append(posted(1, channelA))
		h.Raise(orgA, 1)
		got := <-done
		if got.cursor != 1 || !errors.Is(got.err, context.Canceled) || reads != 1 || len(frames) != 2 || frames[1].ID != 1 {
			t.Fatalf("disconnect: Run=%+v reads=%d frames=%v", got, reads, frames)
		}
		ctx, cancel = context.WithCancel(t.Context())
		go func() { c, err := stream.Run(ctx, selected, got.cursor, send); done <- result{c, err} }()
		synctest.Wait()
		cancel()
		got = <-done
		if got.cursor != 1 || !errors.Is(got.err, context.Canceled) || reads != 2 || len(frames) != 3 || !slices.Equal(frames[2].Data, []byte{2}) {
			t.Fatalf("reconnect: Run=%+v reads=%d frames=%v", got, reads, frames)
		}
		for _, i := range []int{0, 2} {
			if !frames[i].Ephemeral || frames[i].ID != 0 || frames[i].Name != "sidebar" {
				t.Fatal("recount changed the durable SSE cursor")
			}
		}
	})
}

func TestSidebarContinuingBacklog(t *testing.T) {
	testSidebarContinuingBacklog(t, false)
}

func TestSidebarReadChangeBacklog(t *testing.T) {
	testSidebarContinuingBacklog(t, true)
}

func testSidebarContinuingBacklog(t *testing.T, readChanges bool) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		selected := sub
		selected.Interests = []Interest{InterestSidebar}
		w := newSidebarWatch()
		var times []time.Time
		stream := Stream{Hub: NewHub(), Events: slowFilteredLog{SidebarInterval / 4}, BatchSize: 1,
			Authorizer: authorizerFunc(allowAll), Sidebar: sidebarSource{sidebarTrigger,
				func(context.Context, Subscription) (Outgoing, error) {
					times = append(times, time.Now())
					return Outgoing{}, nil
				}}}
		if readChanges {
			stream.Sidebar = watchedSidebar{sidebarSource{func(Event) bool { return false }, stream.Sidebar.(sidebarSource).recount}, func(kernel.ID, kernel.ID) SidebarWatch { return w }}
			stream.Events = sidebarReader(func(ctx context.Context, org kernel.ID, after int64, limit int) ([]Event, error) {
				if after >= 12 {
					return nil, errTooManyReads
				}
				w.publish()
				return (slowFilteredLog{SidebarInterval / 4}).EventsAfter(ctx, org, after, limit)
			})
		}
		cursor, err := stream.Run(ctx, selected, 0, frameSender{func(Outgoing) error {
			if len(times) == 3 {
				cancel()
			}
			return nil
		}})
		if !errors.Is(err, context.Canceled) || cursor != 9 || len(times) != 3 || times[1].Sub(times[0]) != SidebarInterval || times[2].Sub(times[1]) != SidebarInterval {
			t.Fatalf("Run=%d,%v recount times=%v", cursor, err, times)
		}
	})
}

func TestSidebarDeliverySafety(t *testing.T) { testSidebarDeliverySafety(t, false) }

func testSidebarDeliverySafety(t *testing.T, scheduled bool) {
	for _, name := range []string{"deny after render", "replacement during render", "authorization error", "send failure", "cancel during render", "cancel during authorization"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				failure, rendered, checks := errors.New("delivery failed"), false, 0
				selected, send := sub, newRecorder()
				selected.Interests = []Interest{InterestSidebar}
				selected.Member = kernel.ID{1}
				member := selected.Member
				gate, resume := make(chan struct{}), make(chan struct{})
				w := newSidebarWatch()
				send.failOn = -1
				if name == "send failure" {
					send.failOn, send.err = 0, failure
				}
				stream := Stream{Hub: NewHub(), Events: &fakeLog{}, Heartbeat: 2 * SidebarInterval,
					Sidebar: sidebarSource{sidebarTrigger, func(context.Context, Subscription) (Outgoing, error) {
						rendered = true
						close(gate)
						<-resume
						if name == "cancel during render" {
							cancel()
						}
						return Outgoing{}, nil
					}}, Authorizer: authorizerFunc(func(e Event) (bool, error) {
						checks++
						if !rendered || e.OrganizationID != selected.Organization || e.ChannelID != (kernel.ID{}) || e.AudienceMemberID == nil || *e.AudienceMemberID != selected.Member {
							t.Error("authorization must follow rendering and use organisation/member scope")
						}
						if name == "authorization error" {
							return false, failure
						}
						if name == "cancel during authorization" {
							cancel()
						}
						return member != (kernel.ID{}) && (e.AudienceMemberID == nil || *e.AudienceMemberID == member), nil
					})}
				stream.Sidebar = watchedSidebar{stream.Sidebar.(sidebarSource), func(kernel.ID, kernel.ID) SidebarWatch { return w }}
				done := make(chan result, 1)
				stream = compatibilityStream(t, stream, &selected, scheduled)
				go func() { c, err := stream.Run(ctx, selected, 7, send); done <- result{c, err} }()
				<-gate
				if name == "deny after render" {
					member = kernel.ID{}
				}
				if name == "replacement during render" {
					member = kernel.ID{2}
				}
				close(resume)
				synctest.Wait()
				cancel()
				got := <-done
				want, wantChecks := error(context.Canceled), 1
				if name == "deny after render" || name == "replacement during render" {
					want = ErrMembershipRevoked
					if send.heartbeatCount() != 0 {
						t.Fatal("revoked recount sent a heartbeat")
					}
				}
				if name == "authorization error" || name == "send failure" {
					want = failure
				}
				if name == "cancel during render" {
					wantChecks = 0
				}
				if got.cursor != 7 || !errors.Is(got.err, want) || checks != wantChecks || len(send.ids()) != 0 || w.releases != 1 {
					t.Fatalf("Run=%+v checks=%d sent=%v", got, checks, send.ids())
				}
			})
		})
	}
}
