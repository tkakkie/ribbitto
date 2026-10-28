package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"
)

// Each caller publishes creation/start/release under a mutex; real reservations
// and starts still reconcile independently through production budget code.
type budgetStartWorld struct {
	mu         sync.Mutex
	containers map[string]budgetContainer
	starts     int
}

func (w *budgetStartWorld) budgetList(context.Context) ([]budgetContainer, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var result []budgetContainer
	for _, c := range w.containers {
		result = append(result, budgetStartClone(c))
	}
	return result, nil
}
func (w *budgetStartWorld) budgetInspect(_ context.Context, id string) (budgetContainer, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	c, ok := w.containers[id]
	if !ok {
		return c, fmt.Errorf("missing test container")
	}
	return budgetStartClone(c), nil
}
func (w *budgetStartWorld) put(c budgetContainer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.containers[c.ID] = c
}
func (w *budgetStartWorld) remove(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.containers, id)
}

func budgetWiredLocks(t *testing.T, locks budgetLocks) (budgetLocks, func()) {
	t.Helper()
	own := locks
	own.limit = 20 * time.Millisecond
	own.poll = time.Millisecond
	var opened []*os.File
	own.acquired = func(_ budgetLock, f *os.File) { opened = append(opened, f) }
	assertClosed := func() {
		for _, f := range opened {
			if f.Fd() != ^uintptr(0) {
				t.Error("wired partial set held while sleeping")
			}
		}
		opened = nil
	}
	own.aboutToSleep = assertClosed
	// Independently check the sleep boundary too: disabling the hook must not
	// let a partial hold escape this wired concurrency test.
	own.sleep = func(ctx context.Context, d time.Duration) error { assertClosed(); return budgetSleep(ctx, d) }
	return own, assertClosed
}

func budgetWiredNoDeadlock(t *testing.T) {
	locks := budgetLockFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	world := &budgetStartWorld{containers: make(map[string]budgetContainer)}
	runner := containerRunnerFunc(func(_ context.Context, args, _ []string, _, _ io.Writer) (int, error) {
		world.mu.Lock()
		defer world.mu.Unlock()
		if len(args) != 2 || args[0] != "start" {
			t.Errorf("unexpected mutation: %v", args)
			return 1, nil
		}
		c, ok := world.containers[args[1]]
		if !ok {
			t.Error("start missing container")
			return 1, nil
		}
		c.State = "running"
		world.containers[c.ID] = c
		world.starts++
		return 0, nil
	})
	// Guarantee a partial acquisition attempt before the concurrent loops. The
	// independent sleep check must catch retention even with the hook disabled.
	holder := budgetTake(t, locks, budgetSlot2)
	own, assertClosed := budgetWiredLocks(t, locks)
	sleep := own.sleep
	own.sleep = func(ctx context.Context, d time.Duration) error {
		err := sleep(ctx, d)
		holder()
		return err
	}
	target := budgetStartTarget()
	target.ID = fmt.Sprintf("%064x", 100)
	target.Labels[otherBudgetLabel] = "exclusive"
	delete(target.Labels, budgetSlotLabel)
	world.put(target)
	warmup := budgetStart{world, &budgetFakeProcesses{}, own, budgetStartDocker{runner}, time.Second, time.Second}
	if check := warmup.start(ctx, target.ID); check != "" {
		t.Fatalf("wired partial-acquisition start: %s", check)
	}
	world.remove(target.ID)
	assertClosed()

	var wg sync.WaitGroup
	for worker := range 5 {
		wg.Go(func() {
			own, assertClosed := budgetWiredLocks(t, locks)
			for iteration := range 3 {
				id := fmt.Sprintf("%064x", worker*10+iteration+1)
				if worker == 1 || worker == 2 {
					target := budgetStartTarget()
					target.ID = id
					if worker == 1 {
						target.Labels[otherBudgetLabel] = "exclusive"
						delete(target.Labels, budgetSlotLabel)
					}
					world.put(target)
					b := budgetStart{world, &budgetFakeProcesses{}, own, budgetStartDocker{runner}, 20 * time.Millisecond, 20 * time.Millisecond}
					result := b.start(ctx, id)
					if result != "" && result != "busy" {
						t.Errorf("wired start: %s", result)
					}
					world.remove(id)
				} else {
					request := budgetSingle
					if worker == 0 {
						request = budgetBrowserRequest
					}
					slots := budgetSlots{world, &budgetFakeProcesses{}, own, budgetOtherHook}
					r, cause := slots.budgetReserve(ctx, request)
					if r.release != nil {
						c := budgetContainerFixture(r.label)
						c.ID = id
						c.Image = containerVerifyImage
						c.Memory = r.memory
						c.MemorySwap = r.memory
						if request == budgetBrowserRequest {
							c.Labels[otherClassLabel] = "browser"
						}
						world.put(c)
						_ = budgetSleep(ctx, time.Millisecond)
						world.remove(id)
						r.release()
					} else if cause != budgetBusy && cause != budgetOtherResult(otherExclusiveLine) {
						t.Errorf("wired reservation: %s", cause)
					}
				}
				assertClosed()
				if ctx.Err() != nil {
					t.Error("wired callers exceeded context bound")
					return
				}
			}
		})
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("wired callers deadlocked")
	}
	if len(world.containers) != 0 {
		t.Error("wired callers left container reservations behind")
	}
	probe := locks
	probe.limit = 0
	budgetTake(t, probe, budgetBrowser, budgetSlot0, budgetSlot1, budgetSlot2)()
}
