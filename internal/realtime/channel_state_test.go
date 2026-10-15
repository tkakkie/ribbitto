package realtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

type channelSource struct {
	trigger func(Event) bool
	refresh func(context.Context, Subscription) (Outgoing, error)
}

func (s channelSource) Trigger(e Event) bool { return s.trigger(e) }
func (s channelSource) Refresh(ctx context.Context, sub Subscription) (Outgoing, error) {
	return s.refresh(ctx, sub)
}

// Each negative fixture changes one scope; the reader deliberately bypasses
// its own organisation filter so it cannot hide a missing writer predicate.
func TestChannelStateScopesAndDeadline(t *testing.T) {
	for _, name := range []string{"topic", "state only", "organisation", "channel", "interest", "kind"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				selected := sub
				selected.Interests, selected.Topic = []Interest{InterestChannelState, InterestMessages}, &kernel.ID{1}
				if name == "interest" {
					selected.Interests = []Interest{InterestMessages}
				}
				if name == "state only" {
					selected.Interests = []Interest{InterestChannelState}
				}
				h, log, triggers := NewHub(), &fakeLog{}, 0
				var reads atomic.Int64
				stream := Stream{Hub: h, Authorizer: authorizerFunc(allowAll),
					Renderer: rendererFunc(func(Event) (Outgoing, error) {
						t.Fatal("topic filter must precede message rendering")
						return Outgoing{}, nil
					}),
					Events: sidebarReader(func(ctx context.Context, org kernel.ID, after int64, limit int) ([]Event, error) {
						events, err := log.EventsAfter(ctx, org, after, limit)
						if name == "organisation" {
							for i := range events {
								events[i].OrganizationID = orgB
							}
						}
						return events, err
					}), ChannelState: channelSource{func(e Event) bool { triggers++; return e.Kind == kindMoved },
						func(_ context.Context, got Subscription) (Outgoing, error) {
							n := reads.Add(1)
							if got.Organization != selected.Organization || got.Channel != selected.Channel {
								t.Fatal("lost refresh scope")
							}
							return Outgoing{ID: 999, Name: "state", Data: []byte{byte(n)}}, nil
						}}}
				var frames []Outgoing
				send := frameSender{func(out Outgoing) error { frames = append(frames, out); return nil }}
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan result, 1)
				go func() { c, err := stream.Run(ctx, selected, 0, send); done <- result{c, err} }()
				synctest.Wait()
				for seq := int64(1); seq <= 3; seq++ {
					e := posted(seq, channelA)
					e.Kind, e.Topics = kindMoved, []kernel.ID{{2}}
					if name == "channel" {
						e.ChannelID = channelB
					}
					if name == "kind" {
						e.Kind = kindJoined
					}
					log.append(e)
					h.Raise(orgA, seq)
					synctest.Wait()
					time.Sleep(ChannelStateInterval / 4)
				}
				want := 1
				if name == "interest" {
					want = 0
				}
				if reads.Load() != int64(want) {
					t.Fatalf("burst refreshed early: %d", reads.Load())
				}
				time.Sleep(ChannelStateInterval / 4) // Quiet tail must keep the first deadline.
				synctest.Wait()
				if name == "topic" || name == "state only" {
					want++
				}
				if reads.Load() != int64(want) || (name == "organisation" || name == "channel" || name == "interest") && triggers != 0 {
					t.Fatalf("refreshes=%d want=%d triggers=%d", reads.Load(), want, triggers)
				}
				cancel()
				got := <-done
				if got.cursor != 3 || !errors.Is(got.err, context.Canceled) {
					t.Fatalf("Run=%+v", got)
				}
				// Reconnect with no later event must still replace the whole state.
				ctx, cancel = context.WithCancel(t.Context())
				go func() { c, err := stream.Run(ctx, selected, 3, send); done <- result{c, err} }()
				synctest.Wait()
				cancel()
				got = <-done
				if name != "interest" {
					want++
				}
				if reads.Load() != int64(want) || got.cursor != 3 || !errors.Is(got.err, context.Canceled) {
					t.Fatalf("reconnect=%+v refreshes=%d want=%d", got, reads.Load(), want)
				}
				states := 0
				for _, out := range frames {
					if out.Name == "state" {
						states++
						if out.ID != 0 || !out.Ephemeral || len(out.Data) != 1 || out.Data[0] != byte(states) {
							t.Fatal("state lost freshness or changed SSE cursor")
						}
					}
				}
				if states != want || log.readCount() != 5 {
					t.Fatalf("states=%d reads=%d", states, log.readCount())
				}
			})
		})
	}
}

func TestChannelStateBacklogAndTriggerDuringRefresh(t *testing.T) {
	for _, backlog := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			h, log := NewHub(), &fakeLog{}
			selected := sub
			selected.Interests = []Interest{InterestChannelState}
			var times []time.Time
			stream := Stream{Hub: h, Events: log, BatchSize: 1, Authorizer: authorizerFunc(allowAll),
				ChannelState: channelSource{func(Event) bool { return true }, func(context.Context, Subscription) (Outgoing, error) {
					times = append(times, time.Now())
					// Commit after the snapshot was taken, before this refresh sends.
					log.append(posted(int64(len(times)), channelA))
					h.Raise(orgA, int64(len(times)))
					return Outgoing{Name: "state"}, nil
				}}}
			if backlog {
				stream.Events = sidebarReader(func(_ context.Context, _ kernel.ID, after int64, _ int) ([]Event, error) {
					time.Sleep(ChannelStateInterval / 4)
					return []Event{posted(after+1, channelA)}, nil
				})
			}
			cursor, err := stream.Run(ctx, selected, 0, frameSender{func(out Outgoing) error {
				if out.Name == "state" && len(times) == 3 {
					cancel()
				}
				return nil
			}})
			wantCursor := int64(2)
			if backlog {
				wantCursor = 9
			}
			if !errors.Is(err, context.Canceled) || cursor != wantCursor || len(times) != 3 || times[1].Sub(times[0]) != ChannelStateInterval || times[2].Sub(times[1]) != ChannelStateInterval {
				t.Fatalf("backlog=%v Run=%d,%v refresh times=%v", backlog, cursor, err, times)
			}
		})
	}
}

func TestChannelStateFailuresAndCursor(t *testing.T) {
	for _, name := range []string{"read", "state read", "render", "cancel during refresh", "send", "channel denied", "member denied", "authorization", "reset", "message render", "message send"} {
		t.Run(name, func(t *testing.T) {
			failure, reads, sent, rendered := errors.New("failed"), 0, 0, false
			canceled := errors.New("session revoked during refresh")
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			selected := sub
			selected.Member, selected.Interests = kernel.ID{1}, []Interest{InterestChannelState, InterestMessages}
			log := &fakeLog{events: []Event{posted(8, channelA)}}
			want := failure
			if name == "cancel during refresh" {
				want = canceled
			}
			if name == "read" {
				log.failOn, log.err = 1, failure
			}
			if name == "reset" {
				log.boundary, want = 8, nil
			}
			stream := Stream{Hub: NewHub(), Events: log, ChannelState: channelSource{func(Event) bool { return true },
				func(context.Context, Subscription) (Outgoing, error) {
					reads++
					if name == "cancel during refresh" {
						cancel(canceled)
					}
					if name == "state read" || name == "render" {
						return Outgoing{}, failure
					}
					rendered = true
					return Outgoing{ID: 999, Name: "state"}, nil
				}}, Authorizer: authorizerFunc(func(e Event) (bool, error) {
				if ctx.Err() != nil {
					t.Error("authorization ran after cancellation")
					return false, failure
				}
				if name != "reset" && (!rendered || e.OrganizationID != selected.Organization || e.ChannelID != selected.Channel || e.AudienceMemberID == nil || *e.AudienceMemberID != selected.Member) {
					t.Fatal("authorization lost scope or ran before render")
				}
				if name == "authorization" {
					return false, failure
				}
				allowed := (name != "channel denied" || e.ChannelID != channelA) && (name != "member denied" || e.AudienceMemberID == nil || *e.AudienceMemberID != selected.Member)
				return allowed, nil
			}), Renderer: rendererFunc(func(e Event) (Outgoing, error) {
				if name == "message render" {
					return Outgoing{}, failure
				}
				return render(e)
			})}
			cursor, err := stream.Run(ctx, selected, 7, frameSender{func(out Outgoing) error {
				if name == "send" || out.Name == "message" {
					return failure
				}
				if out.Name == "state" && (out.ID != 0 || !out.Ephemeral) {
					t.Fatal("state advanced past undelivered message")
				}
				sent++
				return nil
			}})
			if name == "channel denied" || name == "member denied" {
				want = ErrMembershipRevoked
			}
			wantReads, wantSent := 1, 0
			if name == "read" || name == "reset" {
				wantReads = 0
			}
			if name == "reset" || name == "message render" || name == "message send" {
				wantSent = 1
			}
			if cursor != 7 || !errors.Is(err, want) || reads != wantReads || sent != wantSent {
				t.Fatalf("Run=%d,%v reads=%d sent=%d", cursor, err, reads, sent)
			}
		})
	}
}

func TestChannelStateRevokedDuringRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		selected := sub
		selected.Interests = []Interest{InterestChannelState}
		reads, sent, allowed := 0, 0, true
		stream := Stream{Hub: NewHub(), Events: &fakeLog{events: []Event{posted(1, channelA)}},
			Authorizer: authorizerFunc(func(Event) (bool, error) { return allowed, nil }),
			ChannelState: channelSource{func(Event) bool { return true }, func(context.Context, Subscription) (Outgoing, error) {
				reads++
				allowed = reads == 1 // Access is lost during the second read/render.
				return Outgoing{Name: "state"}, nil
			}}}
		cursor, err := stream.Run(t.Context(), selected, 0, frameSender{func(out Outgoing) error {
			if out.Name == "state" {
				sent++
			}
			return nil
		}})
		if cursor != 1 || !errors.Is(err, ErrMembershipRevoked) || reads != 2 || sent != 1 {
			t.Fatalf("Run=%d,%v reads=%d sent=%d", cursor, err, reads, sent)
		}
	})
}
