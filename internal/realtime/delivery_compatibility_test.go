package realtime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// A gated connect recount exercises registration, submission and the combined
// wait without adding sidebar writes to the existing delivery assertions.
func compatibilityStream(t *testing.T, s Stream, selected *Subscription, scheduled bool) Stream {
	t.Helper()
	if !scheduled {
		return s
	}
	q := NewRecountScheduler(1, 1)
	t.Cleanup(q.Close)
	s.Recounts = q
	if s.Sidebar == nil {
		selected.Interests = append(slices.Clone(selected.Interests), InterestSidebar)
		s.Sidebar = sidebarSource{func(Event) bool { return false }, func(ctx context.Context, _ Subscription) (Outgoing, error) {
			<-ctx.Done()
			return Outgoing{}, context.Cause(ctx)
		}}
	}
	return s
}

func runCompatibilityAsync(ctx context.Context, t *testing.T, s Stream, cursor int64, send Sender, scheduled bool, reserved ...bool) <-chan result {
	selected := sub
	s = compatibilityStream(t, s, &selected, scheduled)
	done := make(chan result, 1)
	go func() {
		c, err := runCompatibility(ctx, s, selected, cursor, send, len(reserved) > 0 && reserved[0])
		done <- result{c, err}
	}()
	return done
}

func runCompatibility(ctx context.Context, s Stream, selected Subscription, cursor int64, send Sender, reserved bool) (int64, error) {
	if reserved {
		return s.RunReserved(ctx, selected, cursor, send, s.Recounts.Register())
	}
	return s.Run(ctx, selected, cursor, send)
}

func TestReservedDeliveryCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*testing.T, bool)
	}{
		{"cold hub", func(t *testing.T, reserved bool) { testStreamColdHubEntry(t, true, reserved) }},
		{"render revocation", func(t *testing.T, reserved bool) { testStreamRenderRevocationEntry(t, true, reserved) }},
		{"authorization frames", testScheduledAuthorizationFrames},
		{"audience cursor", testScheduledAuthorizationAudienceCursor},
		{"exit gates", testScheduledAuthorizationExitGates},
		{"watch publications", testScheduledWatchPublications},
	} {
		t.Run(tc.name, compatibilityCase(tc.name == "cold hub" || tc.name == "render revocation", func(t *testing.T) { tc.run(t, true) }))
	}
}

func compatibilityCase(scheduled bool, run func(*testing.T)) func(*testing.T) {
	return func(t *testing.T) {
		if scheduled {
			synctest.Test(t, run)
		} else {
			run(t)
		}
	}
}

func TestScheduledDeliveryCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name   string
		run    func(*testing.T, bool)
		bubble bool
	}{
		{"cold hub", testStreamDrainsBatchesOnAColdHub, true},
		{"each durable send", testStreamRechecksAccessBeforeEachSend, true},
		{"render revocation", testStreamDeniesAccessLostWhileRendering, true},
		{"sidebar safety", testSidebarDeliverySafety, false},
		{"errors", testStreamErrorsDoNotAdvanceTheCursor, false},
		{"negative cursor", testStreamRefusesANegativeCursor, true},
		{"cancellation", testStreamChecksCancellationBetweenEvents, true},
		{"gap reset", testStreamResetsOnAGap, false},
		{"boundary reset", testStreamResetsBelowTheBoundary, false},
		{"message filters", testStreamSubscriptionInterest, false},
		{"owner scopes", testEphemeralScopesAndAuthorization, false},
		{"sidebar triggers", testSidebarTriggersAndDeadline, false},
		{"reconnect", testSidebarReconnectAfterMessageBeforeRecount, false},
	} {
		t.Run(tc.name, compatibilityCase(tc.bubble, func(t *testing.T) { tc.run(t, true) }))
	}
}

func TestScheduledOwnersDuringRecount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, h := NewRecountScheduler(1, 2), NewHub()
		defer q.Close()
		blocker, free := q.Register(), make(chan struct{})
		blocker.Submit(1, func(context.Context) (Outgoing, error) { <-free; return Outgoing{}, nil })
		synctest.Wait()
		started, finish := make(chan struct{}), make(chan struct{})
		selected := sub
		selected.Interests = []Interest{InterestSidebar, InterestPresence, InterestTyping}
		selected.PresenceAfter = "page:7"
		reads := map[Interest]int{}
		owners := map[Interest]EphemeralOwner{}
		generation := int64(0)
		for _, kind := range []Interest{InterestPresence, InterestTyping} {
			owners[kind] = ownerFunc(func(s Subscription, after int64) (EphemeralFrame, error) {
				want := generation - 1
				if reads[kind] == 0 {
					want = 0
					if kind == InterestTyping {
						want = -1
					}
				}
				if after != want || s.PresenceAfter != selected.PresenceAfter {
					t.Errorf("%s after=%d, want %d with page token", kind, after, want)
				}
				reads[kind]++
				return EphemeralFrame{Organization: orgA, Channel: channelA, Generation: generation,
					Outgoing: Outgoing{ID: 999, Name: string(kind)}}, nil
			})
		}
		stream := Stream{Hub: h, Events: &fakeLog{}, Recounts: q, Owners: owners, Authorizer: authorizerFunc(allowAll),
			Sidebar: sidebarSource{func(Event) bool { return false }, func(context.Context, Subscription) (Outgoing, error) {
				close(started)
				<-finish
				return Outgoing{ID: 999, Name: "wrong"}, nil
			}}}
		var frames []Outgoing
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan result, 1)
		go func() {
			c, err := stream.Run(ctx, selected, 12, frameSender{func(out Outgoing) error { frames = append(frames, out); return nil }})
			done <- result{c, err}
		}()
		synctest.Wait()
		for generation = 1; generation <= 2; generation++ {
			for _, kind := range []Interest{InterestPresence, InterestTyping} {
				h.RaiseGeneration(orgA, kind, generation)
			}
			synctest.Wait()
			if reads[InterestPresence] != int(generation)+1 || reads[InterestTyping] != int(generation)+1 || len(frames) != 2*(int(generation)+1) {
				t.Errorf("queued/running owner delivery: generation=%d reads=%v frames=%v", generation, reads, frames)
			}
			if generation == 1 {
				close(free)
				<-started
				blocker.Cancel()
			}
		}
		close(finish)
		synctest.Wait()
		if len(frames) != 7 || frames[6].Name != "sidebar" {
			t.Errorf("completion frames=%v, want six owner frames then sidebar", frames)
		}
		for _, out := range frames {
			if !out.Ephemeral || out.ID != 0 {
				t.Errorf("%s frame carried durable id: %+v", out.Name, out)
			}
		}
		cancel()
		if got := <-done; got.cursor != 12 || !errors.Is(got.err, context.Canceled) {
			t.Errorf("Run=%+v, want unchanged cursor 12 and cancellation", got)
		}
	})
}

func TestScheduledOwnerResetTerminates(t *testing.T) {
	for _, kind := range []Interest{InterestPresence, InterestTyping} {
		t.Run(string(kind), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				q, h := NewRecountScheduler(1, 1), NewHub()
				defer q.Close()
				selected := sub
				selected.Interests = []Interest{InterestSidebar, kind}
				generation := int64(0)
				stream := Stream{Hub: h, Events: &fakeLog{}, Recounts: q, Authorizer: authorizerFunc(allowAll),
					Sidebar: sidebarSource{sidebarTrigger, func(ctx context.Context, _ Subscription) (Outgoing, error) {
						<-ctx.Done()
						return Outgoing{}, context.Cause(ctx)
					}},
					Owners: map[Interest]EphemeralOwner{kind: ownerFunc(func(_ Subscription, after int64) (EphemeralFrame, error) {
						name := string(kind)
						if generation > 0 {
							name = "reset"
						}
						return EphemeralFrame{Organization: orgA, Channel: channelA, Generation: generation, Outgoing: Outgoing{ID: 999, Name: name}}, nil
					})}}
				var frames []Outgoing
				done := make(chan result, 1)
				go func() {
					c, err := stream.Run(t.Context(), selected, 12, frameSender{func(out Outgoing) error { frames = append(frames, out); return nil }})
					done <- result{c, err}
				}()
				synctest.Wait()
				generation = 1
				h.RaiseGeneration(orgA, kind, 1)
				got := <-done
				if got.cursor != 12 || got.err != nil || len(frames) != 2 || frames[1].Name != "reset" || frames[1].ID != 0 || !frames[1].Ephemeral {
					t.Fatalf("owner reset: Run=%+v frames=%v", got, frames)
				}
			})
		})
	}
}

// Bounds errors represent both expired and future cursors. Use the same
// reader contract here rather than teaching fakeLog unrelated store behavior.
func TestScheduledFutureCursorAndBatchSize(t *testing.T) {
	for _, batch := range []int{0, 1, 3} {
		t.Run(fmt.Sprintf("batch %d", batch), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				selected := sub
				calls := 0
				stream := compatibilityStream(t, Stream{Hub: NewHub(), BatchSize: batch, Authorizer: authorizerFunc(allowAll),
					Events: sidebarReader(func(_ context.Context, _ kernel.ID, after int64, limit int) ([]Event, error) {
						calls++
						want := batch
						if want == 0 {
							want = DefaultBatchSize
						}
						if after != 11 || limit != want {
							t.Errorf("read after=%d limit=%d, want 11/%d", after, limit, want)
						}
						return nil, ErrCursorExpired // committed sequence is 10
					})}, &selected, true)
				var frames []Outgoing
				cursor, err := stream.Run(t.Context(), selected, 11, frameSender{func(out Outgoing) error { frames = append(frames, out); return nil }})
				if cursor != 11 || err != nil || calls != 1 || len(frames) != 1 || frames[0].ID != 11 || frames[0].Name != "reset" || frames[0].Ephemeral {
					t.Fatalf("future cursor reset: cursor=%d err=%v reads=%d frames=%v", cursor, err, calls, frames)
				}
			})
		})
	}
}
