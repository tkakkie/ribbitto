package realtime_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// The production-only arm cannot infer author or committed read effects. Even
// unknown organisation events reconcile; hypothetical deltas never enter here.
type costHybridConnection struct {
	*costModelConnection
	organization kernel.ID
	incarnation  uint64
	interval     time.Duration
	coverage     *hybridCoverage
	watch        *costHybridWatch
	watcher      realtime.SidebarWatcher
}

func (c *costHybridConnection) Trigger(e realtime.Event) bool {
	return e.OrganizationID == c.organization
}

func (c *costHybridConnection) Watch(organization, member kernel.ID) realtime.SidebarWatch {
	interval := c.interval
	if interval == 0 {
		interval = 30 * time.Second
	}
	source := c.watcher
	if source == nil {
		source = c.costModelConnection
	}
	c.watch = &costHybridWatch{base: source.Watch(organization, member), wake: make(chan struct{}), stop: make(chan struct{}), done: make(chan struct{})}
	ticker := time.NewTicker(interval)
	go func() {
		defer close(c.watch.done)
		defer ticker.Stop()
		for {
			_, wake := c.watch.base.Snapshot()
			c.watch.Snapshot() // Include publications before this wake was captured.
			select {
			case <-wake:
			case <-ticker.C:
				c.watch.repair()
			case <-c.watch.stop:
				return
			}
		}
	}()
	return c.watch
}

func (c *costHybridConnection) Recount(ctx context.Context, sub realtime.Subscription) (realtime.Outgoing, error) {
	generation, _ := c.watch.Snapshot()
	out, err := c.costModelConnection.Recount(ctx, sub)
	if err != nil {
		return out, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	frame := c.pending[string(out.Data)]
	frame.incarnation, frame.watch = c.incarnation, generation
	c.pending[string(out.Data)] = frame
	return out, nil
}

func (c *costHybridConnection) Send(ctx context.Context, out realtime.Outgoing) error {
	if out.Name != "sidebar" {
		return c.sender.Send(ctx, out)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	frame, ok := c.pending[string(out.Data)]
	if !ok || out.ID != 0 || !out.Ephemeral {
		return fmt.Errorf("invalid hybrid completion")
	}
	// Preflight on a copy: neither rejected coverage nor a failed send may
	// replace the delivered witness. Only this stream's writer installs it.
	next := *c.coverage
	verdict := next.install(frame)
	if verdict != "applied" {
		delete(c.pending, string(out.Data))
		if verdict == "repair" {
			c.watch.repair()
		}
		return nil
	}
	if err := c.sender.Send(ctx, out); err != nil {
		return err
	}
	*c.coverage, c.latest = next, next.frame
	delete(c.pending, string(out.Data))
	return nil
}

// Repairs stay private to a connection: publishing to the shared member bus
// would dirty the baseline arm and every other tab, skewing matched workloads.
type costHybridWatch struct {
	base              realtime.SidebarWatch
	mu                sync.Mutex
	generation, reads uint64
	wake              chan struct{}
	stop, done        chan struct{}
	release           sync.Once
	stopped           bool
}

func (w *costHybridWatch) publish(n uint64) {
	w.generation += n
	close(w.wake)
	w.wake = make(chan struct{})
}

func (w *costHybridWatch) Snapshot() (uint64, <-chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	reads := w.reads
	if !w.stopped {
		reads, _ = w.base.Snapshot()
	}
	if reads > w.reads {
		w.publish(reads - w.reads)
		w.reads = reads
	}
	return w.generation, w.wake
}

func (w *costHybridWatch) repair() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.publish(1)
}

func (w *costHybridWatch) Release() {
	w.release.Do(func() {
		w.mu.Lock()
		w.stopped = true // Seal a worker entering Recount concurrently with exit.
		w.mu.Unlock()
		close(w.stop)
		<-w.done
		w.base.Release()
	})
}
