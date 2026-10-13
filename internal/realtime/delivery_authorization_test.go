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

// A check grants one write only. Rendering invalidates any earlier grant;
// the sender consumes it, detecting both cached allows and checks before render.
func TestScheduledAuthorizationFrames(t *testing.T) {
	testScheduledAuthorizationFrames(t, false)
}

func testScheduledAuthorizationFrames(t *testing.T, reserved bool) {
	for _, name := range []string{"sidebar", "heartbeat", "reset", "presence", "typing", "presence reset", "typing reset"} {
		t.Run(name, compatibilityCase(true, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			selected, h, log := sub, NewHub(), &fakeLog{}
			selected.Member = kernel.ID{1}
			selected.Interests = []Interest{InterestSidebar, InterestPresence, InterestTyping}
			q := NewRecountScheduler(1, 1)
			defer q.Close()
			w, finish := newSidebarWatch(), make(chan struct{})
			ready, generation, checks := false, int64(0), 0
			var revoked atomic.Bool
			var writes atomic.Int64
			var sent []string
			stream := Stream{Hub: h, Events: log, Recounts: q,
				Sidebar: watchedSidebar{sidebarSource{sidebarTrigger, func(ctx context.Context, _ Subscription) (Outgoing, error) {
					select {
					case <-finish:
					case <-ctx.Done():
						return Outgoing{}, context.Cause(ctx)
					}
					ready = false
					return Outgoing{}, nil
				}}, func(kernel.ID, kernel.ID) SidebarWatch { return w }},
				Authorizer: authorizerFunc(func(e Event) (bool, error) {
					checks++
					wantChannel := kernel.ID{}
					if name == "typing" || name == "typing reset" {
						wantChannel = channelA
					}
					if e.OrganizationID != orgA || e.ChannelID != wantChannel || e.AudienceMemberID == nil || *e.AudienceMemberID != selected.Member {
						t.Error("check must use the bound member and frame scope")
					}
					ready = !revoked.Load()
					return !revoked.Load(), nil
				})}
			if name == "reset" {
				stream.Events = sidebarReader(func(ctx context.Context, org kernel.ID, after int64, limit int) ([]Event, error) {
					ready = false // An allow taken before the replay read is stale.
					return log.EventsAfter(ctx, org, after, limit)
				})
			}
			stream.Owners = make(map[Interest]EphemeralOwner)
			for _, kind := range []Interest{InterestPresence, InterestTyping} {
				stream.Owners[kind] = ownerFunc(func(Subscription, int64) (EphemeralFrame, error) {
					out := ""
					if generation > 0 && (name == string(kind) || name == string(kind)+" reset") {
						out = string(kind)
						if name != string(kind) {
							out = "reset"
						}
						ready = false
					}
					return EphemeralFrame{Organization: orgA, Channel: channelA, Generation: generation, Outgoing: Outgoing{Name: out}}, nil
				})
			}
			if name == "heartbeat" {
				stream.Heartbeat = SidebarInterval
			}
			write := func(frame string) error {
				if !ready || checks != len(sent)+1 {
					t.Errorf("%s send lacks a fresh immediately preceding check: checks=%d sends=%v", frame, checks, sent)
				}
				ready = false
				sent = append(sent, frame)
				writes.Add(1)
				return nil
			}
			done := make(chan result, 1)
			go func() {
				c, err := runCompatibility(ctx, stream, selected, 7, authorizationSender{write}, reserved)
				done <- result{c, err}
			}()
			synctest.Wait()
			reset := name == "reset" || name == "presence reset" || name == "typing reset"
			for attempt := 1; attempt <= 3; attempt++ {
				revoked.Store(attempt == 3)
				switch name {
				case "sidebar":
					finish <- struct{}{}
				case "heartbeat":
					<-time.After(SidebarInterval)
				case "reset":
					log.expire(8)
					h.Raise(orgA, 8)
				default:
					generation++
					h.RaiseGeneration(orgA, InterestPresence, generation)
					h.RaiseGeneration(orgA, InterestTyping, generation)
				}
				synctest.Wait()
				if reset {
					break
				}
				if attempt < 3 {
					if writes.Load() != int64(attempt) {
						t.Fatalf("missing %s frame %d: writes=%d", name, attempt, writes.Load())
					}
					if name == "sidebar" {
						w.publish()
						<-time.After(SidebarInterval)
						synctest.Wait()
					}
				}
			}
			var got result
			select {
			case got = <-done:
			default:
				t.Error("revoked frame did not end stream")
				cancel()
				synctest.Wait()
				got = <-done
			}
			wantErr, wantFrames := error(ErrMembershipRevoked), 2
			if reset {
				wantErr, wantFrames = nil, 1
			}
			if got.cursor != 7 || !errors.Is(got.err, wantErr) || len(sent) != wantFrames || checks != wantFrames+map[bool]int{true: 0, false: 1}[reset] {
				t.Fatalf("frame authorization: Run=%+v checks=%d sends=%v", got, checks, sent)
			}
		}))
	}
}

type authorizationSender struct{ write func(string) error }

func (s authorizationSender) Send(_ context.Context, out Outgoing) error { return s.write(out.Name) }
func (s authorizationSender) Heartbeat(context.Context) error            { return s.write("heartbeat") }

func TestScheduledAuthorizationAudienceCursor(t *testing.T) {
	testScheduledAuthorizationAudienceCursor(t, false)
}

func testScheduledAuthorizationAudienceCursor(t *testing.T, reserved bool) {
	for _, name := range []string{"audience deny", "bound revoke", "audience error", "bound error"} {
		t.Run(name, compatibilityCase(true, func(t *testing.T) {
			failure := errors.New("authorization failed")
			event := posted(8, channelA)
			event.AudienceMemberID = &kernel.ID{2}
			selected := sub
			selected.Member = kernel.ID{1}
			var audiences []kernel.ID
			stream := compatibilityStream(t, Stream{Hub: NewHub(), BatchSize: 1,
				Events: &fakeLog{events: []Event{event}, maxReads: 1}, Renderer: rendererFunc(render),
				Authorizer: authorizerFunc(func(e Event) (bool, error) {
					audiences = append(audiences, *e.AudienceMemberID)
					bound := *e.AudienceMemberID == selected.Member
					if (name == "audience error" && !bound) || (name == "bound error" && bound) {
						return false, failure
					}
					return bound && name != "bound revoke", nil
				})}, &selected, true)
			send := newRecorder()
			cursor, err := runCompatibility(t.Context(), stream, selected, 7, send, reserved)
			wantCursor, wantErr := int64(7), error(failure)
			if name == "audience deny" {
				wantCursor, wantErr = 8, errTooManyReads
			}
			if name == "bound revoke" {
				wantErr = ErrMembershipRevoked
			}
			wantAudiences := []kernel.ID{{2}, {1}}
			if name == "audience error" {
				wantAudiences = wantAudiences[:1]
			}
			if cursor != wantCursor || !errors.Is(err, wantErr) || len(send.ids()) != 0 || !slices.Equal(audiences, wantAudiences) {
				t.Fatalf("audience cursor: cursor=%d err=%v sent=%v checks=%v; want %d/%v", cursor, err, send.ids(), audiences, wantCursor, wantErr)
			}
		}))
	}
}

// Both computations ignore cancellation until released. Exit must precede a
// late recount return, and a buffered completion cannot outlive a denied render.
func TestScheduledAuthorizationExitGates(t *testing.T) {
	testScheduledAuthorizationExitGates(t, false)
}

func testScheduledAuthorizationExitGates(t *testing.T, reserved bool) {
	for _, boundary := range []string{"recount completion", "running recount", "durable render"} {
		for _, race := range []string{"revoke", "cancel", "error", "cancel error", "authorization error"} {
			t.Run(boundary+"/"+race, compatibilityCase(true, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				q, h, log := NewRecountScheduler(1, 1), NewHub(), &fakeLog{}
				defer q.Close()
				selected := sub
				selected.Member = kernel.ID{1}
				selected.Interests = []Interest{InterestMessages, InterestSidebar, InterestPresence, InterestTyping}
				started, finish, rendering, rendered := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
				failure := errors.New("gated failure")
				var revoked atomic.Bool
				stream := Stream{Hub: h, Events: log, Recounts: q, Heartbeat: SidebarInterval,
					Sidebar: sidebarSource{sidebarTrigger, func(context.Context, Subscription) (Outgoing, error) {
						close(started)
						<-finish
						if race == "error" || race == "cancel error" {
							return Outgoing{}, failure
						}
						return Outgoing{}, nil
					}},
					Renderer: rendererFunc(func(e Event) (Outgoing, error) {
						close(rendering)
						<-rendered
						return render(e)
					}),
					Authorizer: authorizerFunc(func(Event) (bool, error) {
						if revoked.Load() && race == "authorization error" {
							return false, failure
						}
						return !revoked.Load(), nil
					}), Owners: make(map[Interest]EphemeralOwner)}
				generation := int64(0)
				for _, kind := range []Interest{InterestPresence, InterestTyping} {
					stream.Owners[kind] = ownerFunc(func(Subscription, int64) (EphemeralFrame, error) {
						name := ""
						if generation > 0 {
							name = string(kind)
						}
						if generation > 1 {
							name = "reset"
						}
						return EphemeralFrame{Organization: orgA, Channel: channelA, Generation: generation, Outgoing: Outgoing{Name: name}}, nil
					})
				}
				var frames []string
				done := make(chan result, 1)
				go func() {
					c, err := runCompatibility(ctx, stream, selected, 7, authorizationSender{func(name string) error { frames = append(frames, name); return nil }}, reserved)
					done <- result{c, err}
				}()
				<-started
				synctest.Wait()
				if boundary == "durable render" {
					log.append(posted(8, channelA))
					h.Raise(orgA, 8)
					<-rendering
					close(finish)
					synctest.Wait() // Buffer the completion before revocation/cancellation.
				}
				revoked.Store(true)
				if race == "cancel" || race == "cancel error" {
					cancel()
				}
				if boundary == "durable render" {
					generation = 1
					h.RaiseGeneration(orgA, InterestPresence, generation)
					h.RaiseGeneration(orgA, InterestTyping, generation)
				}
				if boundary == "recount completion" {
					close(finish)
				}
				synctest.Wait()
				if boundary == "durable render" {
					close(rendered)
				}
				if boundary == "running recount" {
					<-time.After(SidebarInterval)
				}
				synctest.Wait()
				select {
				case got := <-done:
					want := error(ErrMembershipRevoked)
					if race == "authorization error" {
						want = failure
					}
					if boundary == "recount completion" && race == "error" {
						want = failure
					}
					if race == "cancel" || race == "cancel error" {
						want = context.Canceled
					}
					if got.cursor != 7 || !errors.Is(got.err, want) || len(frames) != 0 {
						t.Errorf("revoked gate: Run=%+v frames=%v; want cursor 7/%v and no frames", got, frames, want)
					}
				default:
					t.Error("revoked gate did not exit before late computation")
				}
				if boundary == "running recount" {
					close(finish)
				}
				log.expire(9)
				h.Raise(orgA, 9)
				h.RaiseGeneration(orgA, InterestPresence, 2)
				h.RaiseGeneration(orgA, InterestTyping, 2)
				<-time.After(2 * SidebarInterval)
				synctest.Wait()
				if len(frames) != 0 {
					t.Errorf("frame survived exit: %v", frames)
				}
				cancel()
			}))
		}
	}
}
