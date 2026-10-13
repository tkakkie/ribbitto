package realtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestReservedConsumedOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := NewRecountScheduler(1, 2)
		defer q.Close()
		r := q.Register()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		selected := sub
		selected.Interests = []Interest{InterestSidebar}
		stream := Stream{Recounts: q, Hub: NewHub(), Events: &fakeLog{}, Sidebar: sidebarSource{sidebarTrigger,
			func(ctx context.Context, _ Subscription) (Outgoing, error) {
				<-ctx.Done()
				return Outgoing{}, ctx.Err()
			}}}
		done := make(chan result, 1)
		go func() { c, err := stream.RunReserved(ctx, selected, 7, nil, r); done <- result{c, err} }()
		synctest.Wait()
		if got := q.Stats(); got.Registrations != 1 || got.RunningWorkers != 1 {
			t.Errorf("reserved entry re-registered or unused: registrations=%d running=%d, want 1/1", got.Registrations, got.RunningWorkers)
		}
		// A concurrent second entry must refuse without cancelling the first owner.
		if c, err := stream.RunReserved(ctx, selected, -1, nil, r); c != -1 || !errors.Is(err, ErrRecountRefused) {
			t.Errorf("second consumption: cursor=%d err=%v, want -1/ErrRecountRefused", c, err)
		}
		if r.ctx.Err() != nil {
			t.Error("second consumption cancelled the first owner")
		}
		cancel()
		if got := <-done; got.cursor != 7 || !errors.Is(got.err, context.Canceled) {
			t.Errorf("reserved exit=%+v, want 7/context.Canceled", got)
		}
		synctest.Wait()
		if q.Stats().Registrations != 0 {
			t.Error("reserved exit retained registration")
		}
		if _, err := stream.RunReserved(ctx, selected, 7, nil, r); !errors.Is(err, ErrRecountRefused) {
			t.Errorf("reuse after exit=%v, want ErrRecountRefused", err)
		}
	})
}

func TestReservedEarlyExits(t *testing.T) {
	for _, exit := range []string{"negative", "watch panic", "reader panic", "reader error", "reset", "send error", "cancel", "recount error", "no interest", "no source"} {
		t.Run(exit, compatibilityCase(true, func(t *testing.T) {
			q, w := NewRecountScheduler(1, 1), newSidebarWatch()
			defer q.Close()
			r, failure := q.Register(), errors.New("gated failure")
			selected, cursor := sub, int64(7)
			selected.Interests = []Interest{InterestSidebar}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stream := Stream{Recounts: q, Hub: NewHub(), Authorizer: authorizerFunc(allowAll),
				Events: sidebarReader(func(context.Context, kernel.ID, int64, int) ([]Event, error) {
					if exit == "no interest" || exit == "no source" {
						if q.Stats().Registrations != 0 || r.ctx.Err() == nil {
							t.Error("non-sidebar entry retained reservation before reading")
						}
					}
					switch exit {
					case "reader panic":
						panic(failure)
					case "reset", "send error":
						return nil, ErrCursorExpired
					case "cancel", "recount error":
						return nil, nil
					}
					return nil, failure
				}), Sidebar: watchedSidebar{sidebarSource{sidebarTrigger, func(context.Context, Subscription) (Outgoing, error) {
					return Outgoing{}, failure
				}}, func(kernel.ID, kernel.ID) SidebarWatch {
					if exit == "watch panic" {
						panic(failure)
					}
					return w
				}}}
			switch exit {
			case "recount error":
				stream.Recounts = nil
			case "negative":
				cursor = -1
			case "cancel":
				cancel()
			case "no interest":
				selected.Interests = nil
			case "no source":
				stream.Sidebar = nil
			}
			var err error
			var caught any
			func() {
				defer func() { caught = recover() }()
				_, err = stream.RunReserved(ctx, selected, cursor, frameSender{func(Outgoing) error {
					if exit == "send error" {
						return failure
					}
					return nil
				}}, r)
			}()
			if exit == "watch panic" || exit == "reader panic" {
				if caught != failure {
					t.Errorf("panic=%v, want failure", caught)
				}
			} else if exit == "reset" {
				if err != nil {
					t.Errorf("reset=%v, want nil", err)
				}
			} else if err == nil {
				t.Error("exit lost its error")
			}
			wantRelease := 1
			if exit == "negative" || exit == "watch panic" || exit == "no interest" || exit == "no source" {
				wantRelease = 0
			}
			if got := q.Stats(); got.Registrations != 0 || r.ctx.Err() == nil || w.releases != wantRelease {
				t.Errorf("early exit leaked reservation/watch: registrations=%d cancelled=%v releases=%d want=%d", got.Registrations, r.ctx.Err(), w.releases, wantRelease)
			}
		}))
	}
}

func TestReservedRefusals(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, other := NewRecountScheduler(1, 1), NewRecountScheduler(1, 1)
		defer q.Close()
		defer other.Close()
		r := other.Register()
		if _, err := (Stream{Recounts: q}).RunReserved(t.Context(), sub, 7, nil, r); !errors.Is(err, ErrRecountRefused) {
			t.Errorf("scheduler mismatch=%v, want ErrRecountRefused", err)
		}
		if other.Stats().Registrations != 0 || q.Stats().Registrations != 0 {
			t.Error("scheduler mismatch leaked or registered")
		}
		for _, registration := range []*RecountRegistration{nil, r} {
			if _, err := (Stream{Recounts: q}).RunReserved(t.Context(), sub, 7, nil, registration); !errors.Is(err, ErrRecountRefused) {
				t.Errorf("missing/cancelled reservation=%v, want ErrRecountRefused", err)
			}
		}
	})
}

func TestReservedRunningExit(t *testing.T) {
	for _, exit := range []string{"cancel", "reader error", "reset", "revoke"} {
		t.Run(exit, compatibilityCase(true, func(t *testing.T) {
			q, h, w := NewRecountScheduler(1, 1), NewHub(), newSidebarWatch()
			defer q.Close()
			finish := make(chan struct{})
			release := sync.OnceFunc(func() { close(finish) })
			defer release()
			r, failure := q.Register(), errors.New("reader failed")
			selected := sub
			selected.Interests = []Interest{InterestSidebar}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ending, writes := false, 0
			stream := Stream{Hub: h, Recounts: q,
				Authorizer: authorizerFunc(func(Event) (bool, error) { return exit != "revoke" || !ending, nil }),
				Events: sidebarReader(func(context.Context, kernel.ID, int64, int) ([]Event, error) {
					if ending {
						if exit == "reader error" {
							return nil, failure
						}
						return nil, ErrCursorExpired
					}
					return nil, nil
				}), Sidebar: watchedSidebar{sidebarSource{sidebarTrigger, func(context.Context, Subscription) (Outgoing, error) {
					<-finish // Intentionally ignore cancellation until actual return.
					return Outgoing{}, nil
				}}, func(kernel.ID, kernel.ID) SidebarWatch { return w }}}
			done := make(chan result, 1)
			go func() {
				c, err := stream.RunReserved(ctx, selected, 7, frameSender{func(Outgoing) error { writes++; return nil }}, r)
				done <- result{c, err}
			}()
			synctest.Wait()
			ending = true
			if exit == "cancel" {
				cancel()
			} else {
				h.Raise(orgA, 8)
			}
			got := <-done
			want := error(ErrMembershipRevoked)
			switch exit {
			case "cancel":
				want = context.Canceled
			case "reader error":
				want = failure
			case "reset":
				want = nil
			}
			if got.cursor != 7 || !errors.Is(got.err, want) {
				t.Errorf("running exit=%+v, want 7/%v", got, want)
			}
			synctest.Wait()
			if stats := q.Stats(); stats.Registrations != 1 || stats.RunningWorkers != 1 || r.ctx.Err() == nil || w.releases != 1 {
				t.Errorf("running exit released capacity early or did not cancel: registrations=%d running=%d cancelled=%v releases=%d", stats.Registrations, stats.RunningWorkers, r.ctx.Err(), w.releases)
			}
			release()
			synctest.Wait()
			wantWrites := 0
			if exit == "reset" {
				wantWrites = 1
			}
			if stats := q.Stats(); stats.Registrations != 0 || stats.RunningWorkers != 0 || writes != wantWrites {
				t.Errorf("actual return leaked capacity or sent late: registrations=%d running=%d writes=%d want=%d", stats.Registrations, stats.RunningWorkers, writes, wantWrites)
			}
		}))
	}
}
