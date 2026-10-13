package realtime

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestScheduledWatchPublications(t *testing.T) {
	testScheduledWatchPublications(t, false)
}

func testScheduledWatchPublications(t *testing.T, reserved bool) {
	for _, phase := range []string{"before subscribe", "after subscribe", "queued", "computing", "completing", "sending", "wait capture", "blocked"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				q, w, h := NewRecountScheduler(1, 2), newSidebarWatch(), NewHub()
				defer q.Close()
				var subscribed atomic.Bool
				var persisted atomic.Uint64
				commit := func() {
					persisted.Add(1)
					if subscribed.Load() {
						w.publish()
					}
				}
				gate, resume := make(chan struct{}), make(chan struct{})
				pause := sync.OnceFunc(func() { close(gate); <-resume })
				if phase == "queued" {
					blocker := q.Register()
					defer blocker.Cancel()
					blocker.Submit(1, func(context.Context) (Outgoing, error) { pause(); return Outgoing{}, nil })
					<-gate
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				selected := sub
				selected.Interests = []Interest{InterestSidebar}
				var reads, counts atomic.Int32
				var frameMu sync.Mutex
				var sent []Outgoing
				framesNow := func() []Outgoing {
					frameMu.Lock()
					defer frameMu.Unlock()
					return slices.Clone(sent)
				}
				stream := Stream{Hub: h, Recounts: q, Authorizer: authorizerFunc(allowAll),
					Events: sidebarReader(func(context.Context, kernel.ID, int64, int) ([]Event, error) {
						read := reads.Add(1)
						if phase == "completing" && read == 2 {
							pause() // Completion received, not yet delivered.
						}
						return nil, nil
					}), Sidebar: watchedSidebar{sidebarSource{sidebarTrigger, func(context.Context, Subscription) (Outgoing, error) {
						if !subscribed.Load() {
							t.Error("scheduled recount preceded subscription")
						}
						value := persisted.Load()
						count := counts.Add(1)
						if phase == "computing" && count == 1 {
							pause()
						}
						return Outgoing{Data: []byte{byte(value)}}, nil
					}}, func(org, member kernel.ID) SidebarWatch {
						if org != selected.Organization || member != selected.Member {
							t.Error("scheduled watch used wrong key")
						}
						subscribed.Store(true)
						if phase == "after subscribe" {
							commit()
						}
						return w
					}}}
				if phase == "before subscribe" {
					commit()
				}
				done := make(chan result, 1)
				go func() {
					cursor, err := runCompatibility(ctx, stream, selected, 7, frameSender{func(out Outgoing) error {
						frameMu.Lock()
						sent = append(sent, out)
						first := len(sent) == 1
						frameMu.Unlock()
						if first {
							if phase == "sending" {
								commit()
							}
							if phase == "wait capture" {
								h.afterWaitRead = pause
							}
						}
						return nil
					}}, reserved)
					done <- result{cursor, err}
				}()
				switch phase {
				case "queued":
					synctest.Wait() // Initial request is queued behind the blocker.
				case "computing", "completing", "wait capture":
					<-gate
				}
				if phase == "queued" || phase == "computing" || phase == "completing" || phase == "wait capture" {
					commit()
					close(resume)
				}
				synctest.Wait()
				if phase == "blocked" {
					commit()
					synctest.Wait()
				}
				frames := framesNow()
				if len(frames) != 1 || counts.Load() != 1 || h.Waiting(orgA) != 1 || reads.Load() > 4 {
					t.Fatalf("publication lost or spun: counts=%v frames=%v reads=%d waiters=%d", counts.Load(), frames, reads.Load(), h.Waiting(orgA))
				}
				initial := phase == "before subscribe" || phase == "after subscribe"
				wantFirst := byte(0)
				if initial || phase == "queued" {
					wantFirst = 1
				}
				if frames[0].Data[0] != wantFirst {
					t.Errorf("initial frame=%v want count=%d", frames[0].Data, wantFirst)
				}
				<-time.After(SidebarInterval - time.Nanosecond)
				synctest.Wait()
				if counts.Load() != 1 {
					t.Error("publication moved recount before fixed deadline")
				}
				<-time.After(time.Nanosecond)
				synctest.Wait()
				want := 2
				if initial {
					want = 1
				}
				frames = framesNow()
				if len(frames) != want || int(counts.Load()) != want || frames[len(frames)-1].Data[0] != 1 {
					t.Errorf("publication did not remain pending: counts=%v frames=%v want=%d", counts.Load(), frames, want)
				}
				cancel()
				if got := <-done; got.cursor != 7 || !errors.Is(got.err, context.Canceled) || w.releases != 1 {
					t.Errorf("scheduled watch exit=%+v releases=%d", got, w.releases)
				}
			})
		})
	}
}

func TestScheduledRequestCapture(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, w := NewRecountScheduler(1, 1), newSidebarWatch()
		defer q.Close()
		p := &scheduledSidebar{registration: q.Register(), pending: &sidebarDelivery{dirty: true, watch: w}, version: 1}
		defer p.cancel()
		stream := Stream{Authorizer: authorizerFunc(allowAll), Sidebar: sidebarSource{sidebarTrigger, func(context.Context, Subscription) (Outgoing, error) { return Outgoing{}, nil }}}
		written, frames := time.Now(), 0
		send := frameSender{func(Outgoing) error { frames++; return nil }}
		_ = stream.scheduledRecount(t.Context(), sub, p, nil, send, &written)
		first := <-p.registration.Completions()
		w.publish()
		// Deliberately permit another submission before delivering the first:
		// its capture must not overwrite the first request's generation.
		p.active = false
		_ = stream.scheduledRecount(t.Context(), sub, p, nil, send, &written)
		second := <-p.registration.Completions()
		_ = stream.scheduledRecount(t.Context(), sub, p, &first, send, &written)
		if p.pending.covered != 0 || !p.pending.dirty {
			t.Errorf("first request borrowed later capture: covered=%d dirty=%v", p.pending.covered, p.pending.dirty)
		}
		_ = stream.scheduledRecount(t.Context(), sub, p, &second, send, &written)
		if p.pending.covered != 1 || p.pending.dirty || frames != 2 || len(p.captures) != 0 {
			t.Errorf("second request lost capture: covered=%d dirty=%v frames=%d retained=%d", p.pending.covered, p.pending.dirty, frames, len(p.captures))
		}
	})
}

func TestScheduledExitSeal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, w := NewRecountScheduler(1, 2), newSidebarWatch()
		defer q.Close()
		blocker, free := q.Register(), make(chan struct{})
		blocker.Submit(1, func(context.Context) (Outgoing, error) { <-free; return Outgoing{}, nil })
		synctest.Wait()
		p := &scheduledSidebar{registration: q.Register(), pending: &sidebarDelivery{dirty: true, watch: w}, version: 1}
		calls := 0
		stream := Stream{Sidebar: sidebarSource{sidebarTrigger, func(context.Context, Subscription) (Outgoing, error) { calls++; return Outgoing{}, nil }}}
		written := time.Now()
		_ = stream.scheduledRecount(t.Context(), sub, p, nil, nil, &written)
		q.mu.Lock()
		compute := p.registration.pending.compute
		q.mu.Unlock()
		p.cancel()
		w.Release()
		snapshots := w.snapshots
		// Model a dispatched callback reaching the boundary only after exit.
		_, err := compute(t.Context())
		if !errors.Is(err, context.Canceled) || w.snapshots != snapshots || calls != 0 {
			t.Errorf("late worker crossed exit seal: err=%v snapshots=%d want=%d calls=%d", err, w.snapshots, snapshots, calls)
		}
		close(free)
		blocker.Cancel()
	})
}

func TestScheduledWatchEarlyExits(t *testing.T) {
	for _, exit := range []string{"negative cursor", "no interest", "reader panic"} {
		t.Run(exit, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				q, w := NewRecountScheduler(1, 1), newSidebarWatch()
				defer q.Close()
				selected, cursor := sub, int64(7)
				selected.Interests = []Interest{InterestSidebar}
				if exit == "negative cursor" {
					cursor = -1
				}
				if exit == "no interest" {
					selected.Interests = nil
				}
				failure := errors.New("reader failed")
				stream := Stream{Hub: NewHub(), Recounts: q,
					Events: sidebarReader(func(context.Context, kernel.ID, int64, int) ([]Event, error) {
						if exit == "reader panic" {
							panic(failure)
						}
						return nil, failure
					}), Sidebar: watchedSidebar{sidebarSource{}, func(kernel.ID, kernel.ID) SidebarWatch { return w }}}
				func() {
					defer func() {
						if got := recover(); got != nil && (exit != "reader panic" || got != failure) {
							t.Errorf("unexpected panic: %v", got)
						}
					}()
					_, _ = stream.Run(t.Context(), selected, cursor, nil)
				}()
				want := 0
				if exit == "reader panic" {
					want = 1
				}
				if w.releases != want || len(q.streams) != 0 {
					t.Errorf("early exit leaked watch/registration: releases=%d want=%d registrations=%d", w.releases, want, len(q.streams))
				}
			})
		})
	}
}
