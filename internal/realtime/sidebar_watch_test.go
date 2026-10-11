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

type watchedSidebar struct {
	sidebarSource
	watch func(kernel.ID, kernel.ID) SidebarWatch
}

func (s watchedSidebar) Watch(org, member kernel.ID) SidebarWatch { return s.watch(org, member) }

// Instrument snapshots as well as reads: an unrelated wake can be swallowed
// inside waitFor without reaching the event reader.
type sidebarWatch struct {
	mu                  sync.Mutex
	generation          uint64
	changed             chan struct{}
	snapshots, releases int
}

func newSidebarWatch() *sidebarWatch { return &sidebarWatch{changed: make(chan struct{})} }
func (w *sidebarWatch) publish() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.generation++
	close(w.changed)
	w.changed = make(chan struct{})
}
func (w *sidebarWatch) Snapshot() (uint64, <-chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.snapshots++
	return w.generation, w.changed
}
func (w *sidebarWatch) Release() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.releases++
}

func TestSidebarWatchRaces(t *testing.T) {
	for _, phase := range []string{"before subscribe", "after subscribe", "snapshot", "send", "wait capture", "blocked"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w, h, log := newSidebarWatch(), NewHub(), &fakeLog{maxReads: 5}
				selected := sub
				selected.Member, selected.Interests = kernel.ID{0x11}, []Interest{InterestSidebar}
				acquired, counts := false, []uint64{}
				// Persisted state and the watch are separate: a commit before
				// any subscriber publishes nothing (the registry keeps no entry)
				// and only the initial recount's persisted read can cover it.
				var persisted atomic.Uint64
				commit := func() {
					persisted.Add(1)
					if acquired {
						w.publish()
					}
				}
				gate, resume := make(chan struct{}), make(chan struct{})
				source := watchedSidebar{sidebarSource{sidebarTrigger, func(context.Context, Subscription) (Outgoing, error) {
					if !acquired {
						t.Error("recount preceded subscription")
					}
					value := persisted.Load() // The persisted read, before rendering.
					counts = append(counts, value)
					if phase == "snapshot" && len(counts) == 1 {
						close(gate)
						<-resume
					}
					return Outgoing{ID: 999, Name: "wrong", Data: []byte{byte(value)}}, nil
				}}, func(org, member kernel.ID) SidebarWatch {
					if org != selected.Organization || member != selected.Member {
						t.Error("wrong watch key")
					}
					acquired = true
					if phase == "after subscribe" {
						commit()
					}
					return w
				}}
				if phase == "before subscribe" {
					commit()
				}
				if phase == "wait capture" {
					h.afterWaitRead = sync.OnceFunc(func() { close(gate); <-resume })
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan result, 1)
				var frames []Outgoing
				stream := Stream{Hub: h, Events: log, Sidebar: source, Authorizer: authorizerFunc(allowAll)}
				go func() {
					c, err := stream.Run(ctx, selected, 7, frameSender{func(out Outgoing) error {
						frames = append(frames, out)
						if phase == "send" && len(frames) == 1 {
							commit()
						}
						return nil
					}})
					done <- result{c, err}
				}()
				if phase == "snapshot" || phase == "wait capture" {
					<-gate
					commit()
					close(resume)
				}
				synctest.Wait()
				if phase == "blocked" {
					commit()
					synctest.Wait()
				}
				later := phase != "before subscribe" && phase != "after subscribe"
				if len(counts) != 1 || log.readCount() != map[bool]int{false: 1, true: 2}[later] || h.Waiting(orgA) != 1 {
					t.Fatalf("lost wake or spin: counts=%v log reads=%d waiters=%d", counts, log.readCount(), h.Waiting(orgA))
				}
				time.Sleep(SidebarInterval) // Virtual time exercises the fixed deadline.
				synctest.Wait()
				want := 1
				if later {
					want = 2
				}
				if len(counts) != want || counts[want-1] != 1 || len(frames) != want || frames[want-1].Data[0] != 1 {
					t.Fatalf("publication not covered: counts=%v frames=%v", counts, frames)
				}
				for _, out := range frames {
					if out.Name != "sidebar" || out.ID != 0 || !out.Ephemeral {
						t.Error("sidebar changed SSE cursor")
					}
				}
				cancel()
				if got := <-done; got.cursor != 7 || !errors.Is(got.err, context.Canceled) || w.releases != 1 {
					t.Fatalf("Run=%+v releases=%d", got, w.releases)
				}
			})
		})
	}
}

func TestSidebarWatchCoalescingAndIsolation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewHub()
		watches := []*sidebarWatch{newSidebarWatch(), newSidebarWatch(), newSidebarWatch()}
		logs, counts, frames := make([]*fakeLog, 5), make([][]uint64, 5), make([]int, 5)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan result, 5)
		for i := range 5 {
			selected := sub
			selected.Member, selected.Interests = kernel.ID{0x11}, []Interest{InterestSidebar}
			key := 0 // Two tabs of the same member share one invalidation entry.
			if i == 2 {
				selected.Member, key = kernel.ID{2}, 1
			}
			if i == 3 {
				selected.Organization, key = orgB, 2
			}
			if i == 4 {
				selected.Interests = nil
			}
			logs[i] = &fakeLog{maxReads: 50}
			source := watchedSidebar{sidebarSource{func(Event) bool { return false }, func(context.Context, Subscription) (Outgoing, error) {
				value, _ := watches[key].Snapshot()
				counts[i] = append(counts[i], value)
				return Outgoing{}, nil
			}}, func(org, member kernel.ID) SidebarWatch {
				if i == 4 || org != selected.Organization || member != selected.Member {
					t.Error("watch acquired outside sidebar scope")
				}
				return watches[key]
			}}
			stream := Stream{Hub: h, Events: logs[i], Sidebar: source, Authorizer: authorizerFunc(allowAll)}
			go func() {
				c, err := stream.Run(ctx, selected, 7, frameSender{func(Outgoing) error { frames[i]++; return nil }})
				done <- result{c, err}
			}()
		}
		synctest.Wait()
		baseline := []int{watches[1].snapshots, watches[2].snapshots}
		var publications sync.WaitGroup
		start := make(chan struct{})
		for range 32 {
			publications.Go(func() { <-start; watches[0].publish() })
		}
		close(start)
		publications.Wait()
		synctest.Wait()
		time.Sleep(SidebarInterval / 2)
		watches[0].publish()
		synctest.Wait()
		time.Sleep(SidebarInterval/2 - time.Nanosecond)
		synctest.Wait()
		if len(counts[0]) != 1 || len(counts[1]) != 1 {
			t.Fatal("read changes recounted before fixed deadline")
		}
		watches[0].publish()
		synctest.Wait()
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		for i := range 5 {
			want := 1
			if i < 2 {
				want = 2
			}
			if i == 4 {
				want = 0
			}
			if len(counts[i]) != want || frames[i] != want || (i < 2 && counts[i][1] != 34) || (i >= 2 && logs[i].readCount() != 1) {
				t.Fatalf("stream %d: counts=%v frames=%d reads=%d", i, counts[i], frames[i], logs[i].readCount())
			}
		}
		if watches[1].snapshots != baseline[0] || watches[2].snapshots != baseline[1] {
			t.Fatal("unrelated member watch woke inside waitFor")
		}
		cancel()
		for range 5 {
			if got := <-done; got.cursor != 7 || !errors.Is(got.err, context.Canceled) {
				t.Fatalf("Run=%+v", got)
			}
		}
	})
}

func TestSidebarWatchLifetime(t *testing.T) {
	for _, exit := range []string{"reader", "recount", "reset", "panic", "heartbeat", "negative cursor", "no interest"} {
		t.Run(exit, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w, failure := newSidebarWatch(), errors.New("failed")
				selected, cursor := sub, int64(7)
				selected.Interests = []Interest{InterestSidebar}
				if exit == "negative cursor" {
					cursor = -1
				}
				if exit == "no interest" {
					selected.Interests = nil
				}
				stream := Stream{Hub: NewHub(), Heartbeat: SidebarInterval, Authorizer: authorizerFunc(allowAll),
					Events: sidebarReader(func(context.Context, kernel.ID, int64, int) ([]Event, error) {
						if exit == "panic" {
							panic(failure)
						}
						if exit == "reset" {
							return nil, ErrCursorExpired
						}
						if exit == "heartbeat" || exit == "recount" {
							return nil, nil
						}
						return nil, failure
					}), Sidebar: watchedSidebar{sidebarSource{sidebarTrigger, func(context.Context, Subscription) (Outgoing, error) {
						if exit == "recount" {
							return Outgoing{}, failure
						}
						return Outgoing{}, nil
					}}, func(kernel.ID, kernel.ID) SidebarWatch { return w }}}
				send := newRecorder()
				send.failOn, send.heartbeatErr = -1, failure
				func() {
					defer func() {
						if got := recover(); got != nil && (exit != "panic" || got != failure) {
							t.Errorf("panic=%v", got)
						}
					}()
					got, err := stream.Run(t.Context(), selected, cursor, send)
					if got != cursor || (exit != "reset" && exit != "negative cursor" && !errors.Is(err, failure)) {
						t.Errorf("Run=%d,%v", got, err)
					}
				}()
				want := 1
				if exit == "negative cursor" || exit == "no interest" {
					want = 0
				}
				if w.releases != want {
					t.Fatalf("releases=%d want=%d", w.releases, want)
				}
			})
		})
	}
}

func TestSidebarWatchHeartbeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, h, log, send := newSidebarWatch(), NewHub(), &fakeLog{maxReads: 10}, newRecorder()
		send.failOn = -1
		selected := sub
		selected.Interests = []Interest{InterestSidebar}
		counts := 0
		stream := Stream{Hub: h, Events: log, Heartbeat: SidebarInterval / 2,
			Authorizer: authorizerFunc(func(Event) (bool, error) { return false, nil }),
			Sidebar: watchedSidebar{sidebarSource{sidebarTrigger, func(context.Context, Subscription) (Outgoing, error) {
				counts++
				return Outgoing{}, nil
			}}, func(kernel.ID, kernel.ID) SidebarWatch { return w }}}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan result, 1)
		go func() { c, err := stream.Run(ctx, selected, 7, send); done <- result{c, err} }()
		synctest.Wait()
		for range 4 {
			w.publish()
			synctest.Wait()
			time.Sleep(SidebarInterval / 4)
			synctest.Wait()
		}
		if counts != 2 || send.heartbeatCount() != 2 || len(send.ids()) != 0 || log.readCount() != 6 {
			t.Fatalf("read wakes/denies changed deadlines: recounts=%d heartbeats=%d frames=%v log reads=%d", counts, send.heartbeatCount(), send.ids(), log.readCount())
		}
		h.Raise(selected.Organization, 8) // An empty durable wake must not repeat a covered deny.
		synctest.Wait()
		time.Sleep(SidebarInterval)
		synctest.Wait()
		if counts != 2 || send.heartbeatCount() != 4 || log.readCount() != 7 {
			t.Fatalf("covered deny repeated or heartbeat stalled: recounts=%d heartbeats=%d reads=%d", counts, send.heartbeatCount(), log.readCount())
		}
		cancel()
		if got := <-done; got.cursor != 7 || !errors.Is(got.err, context.Canceled) || w.releases != 1 {
			t.Fatalf("Run=%+v releases=%d", got, w.releases)
		}
	})
}
