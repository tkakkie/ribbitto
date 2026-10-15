package realtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestRecountLimit(t *testing.T) {
	for _, name := range []string{"sync"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				limit := NewRecountLimit(2)
				limit.random = func(time.Duration) time.Duration { return 0 }
				var running, peak, calls atomic.Int32
				var hold <-chan struct{}
				gate := make(chan struct{})
				log, hub := &fakeLog{}, NewHub()
				watches := make([]*sidebarWatch, 4)
				sends := make([]*recorder, 4)
				cancels := make([]context.CancelFunc, 4)
				done := make(chan error, 4)
				release := sync.OnceFunc(func() { close(gate) })
				defer func() {
					release()
					for _, cancel := range cancels {
						if cancel != nil {
							cancel()
						}
					}
				}()
				start := func(i int) {
					stream := Stream{Hub: hub, Events: log, RecountLimit: limit, Authorizer: authorizerFunc(allowAll),
						Sidebar: watchedSidebar{sidebarSource{func(e Event) bool { return i == 3 && e.Kind == kindMoved }, func(context.Context, Subscription) (Outgoing, error) {
							n := running.Add(1)
							calls.Add(1)
							for old := peak.Load(); n > old; old = peak.Load() {
								if peak.CompareAndSwap(old, n) {
									break
								}
							}
							if hold != nil {
								<-hold
							}
							running.Add(-1)
							return Outgoing{}, nil
						}}, func(kernel.ID, kernel.ID) SidebarWatch { watches[i] = newSidebarWatch(); return watches[i] }}}
					selected := sub
					selected.Interests = []Interest{InterestSidebar}
					ctx, cancel := context.WithCancel(t.Context())
					cancels[i] = cancel
					go func() { _, err := stream.Run(ctx, selected, 0, sends[i]); done <- err }()
					synctest.Wait()
				}
				for i := range sends {
					sends[i] = newRecorder()
					sends[i].failOn = -1
					start(i)
				}
				if calls.Load() != 4 {
					t.Fatal("initial recounts missing")
				}
				<-time.After(SidebarInterval)
				hold = gate
				cancels[0]()
				synctest.Wait()
				<-done
				start(0) // Connect while read, resync and move computations share the limit.
				watches[1].publish()
				synctest.Wait() // Read advance.
				watches[2].publish()
				synctest.Wait() // Resync uses the same member watch.
				event := posted(1, channelA)
				event.Kind = kindMoved
				log.append(event)
				hub.Raise(orgA, 1)
				synctest.Wait()
				<-time.After(25 * time.Millisecond)
				synctest.Wait()
				if running.Load() != 2 || peak.Load() > 2 || calls.Load() != 6 {
					t.Fatalf("limit exceeded: running=%d peak=%d calls=%d", running.Load(), peak.Load(), calls.Load())
				}
				select {
				case err := <-done:
					t.Fatalf("established stream ended instead of deferring: %v", err)
				default:
				}
				for i := 2; i < 4; i++ {
					if sends[i].count("sidebar") != 1 || sends[i].count("checkpoint") != 1 || len(sends[i].ids()) != 2 {
						t.Fatalf("deferred stream %d sent a recount", i)
					}
				}
				hold = nil // Release the computations, then allow the dirty streams' jittered retry.
				release()
				synctest.Wait()
				<-time.After(time.Second)
				synctest.Wait()
				if calls.Load() != 8 || peak.Load() > 2 {
					t.Fatalf("dirty streams did not retry: calls=%d peak=%d", calls.Load(), peak.Load())
				}
				for i := range sends {
					if sends[i].count("sidebar") != 2 || sends[i].count("checkpoint") != 1 || len(sends[i].ids()) != 3 {
						t.Fatalf("stream %d did not deliver its deferred recount: %v", i, sends[i].ids())
					}
					cancels[i]()
				}
				synctest.Wait()
				for range sends {
					if err := <-done; !errors.Is(err, context.Canceled) {
						t.Fatalf("stream refused work: %v", err)
					}
				}
			})
		})
	}
}

func TestRecountConnectDelay(t *testing.T) {
	t.Run("sync", func(t *testing.T) {
		for _, delay := range []time.Duration{0, 3 * time.Second} {
			synctest.Test(t, func(t *testing.T) {
				limit := NewRecountLimit(1)
				limit.random = func(bound time.Duration) time.Duration {
					if bound != 3*time.Second {
						t.Errorf("connect delay bound = %v, want 3s", bound)
					}
					return delay
				}
				log := &fakeLog{}
				log.append(posted(1, channelA))
				selected := sub
				selected.Interests = []Interest{InterestSidebar, InterestMessages}
				var calls atomic.Int32
				stream := Stream{Hub: NewHub(), Events: log, Renderer: rendererFunc(render), Authorizer: authorizerFunc(allowAll), RecountLimit: limit,
					Sidebar: sidebarSource{sidebarTrigger, func(context.Context, Subscription) (Outgoing, error) { calls.Add(1); return Outgoing{}, nil }}}
				send := newRecorder()
				send.failOn = -1
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan error, 1)
				go func() { _, err := stream.Run(ctx, selected, 0, send); done <- err }()
				synctest.Wait()
				if len(send.ids()) < 1 || send.ids()[0] != 1 {
					t.Fatal("connect delay blocked authorized message delivery")
				}
				if delay > 0 {
					if calls.Load() != 0 {
						t.Fatal("connect recount ran before jitter deadline")
					}
					<-time.After(delay - time.Nanosecond)
					synctest.Wait()
					if calls.Load() != 0 {
						t.Fatal("connect recount ran before 3s")
					}
					<-time.After(time.Nanosecond)
					synctest.Wait()
				}
				if calls.Load() != 1 {
					t.Fatalf("connect recount missed bounded deadline: %d", calls.Load())
				}
				cancel()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				// A revoked membership must end immediately, before the connect delay.
				stream.Events = &fakeLog{}
				stream.Authorizer = authorizerFunc(func(Event) (bool, error) { return false, nil })
				if _, err := stream.Run(t.Context(), selected, 0, newRecorder()); !errors.Is(err, ErrMembershipRevoked) {
					t.Fatalf("authorization delayed: %v", err)
				}
			})
		}
	})
}
