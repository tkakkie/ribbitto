package realtime_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/realtime"
)

type costQueueGate struct {
	started chan int64
	release chan struct{}
}

func newCostQueueGate() costQueueGate {
	return costQueueGate{make(chan int64, 16), make(chan struct{})}
}

func (g costQueueGate) request(version int64) costQueueRequest {
	return costQueueRequest{version, func(ctx context.Context) (realtime.Outgoing, error) {
		g.started <- version
		select {
		case <-g.release:
			return realtime.Outgoing{ID: version}, nil
		case <-ctx.Done():
			return realtime.Outgoing{}, context.Cause(ctx)
		}
	}}
}

func (g costQueueGate) want(t *testing.T, version int64) {
	t.Helper()
	synctest.Wait()
	select {
	case got := <-g.started:
		if got != version {
			t.Errorf("FIFO/latest admission = %d, want %d", got, version)
		}
	default:
		t.Error("gated request did not start")
	}
}

func TestCostQueueFairReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, g := newCostQueue(1, 3), newCostQueueGate()
		defer q.shutdown()
		busy, quiet, later := q.register(), q.register(), q.register()
		q.submit(busy, g.request(1))
		g.want(t, 1)
		q.submit(busy, g.request(2))
		// Fake time advances only to measure age; channel gates order all work.
		time.Sleep(2 * time.Second)
		q.submit(quiet, g.request(10))
		q.submit(later, g.request(20))
		q.submit(busy, g.request(3))
		if got := q.stats(); got.queued != 3 || got.coalesced != 1 || got.oldest != 2*time.Second {
			t.Errorf("replacement reset wait age or grew queue: %+v", got)
		}
		for _, version := range []int64{10, 20, 3} {
			g.release <- struct{}{}
			g.want(t, version)
		}
		g.release <- struct{}{}
		synctest.Wait()
		if got := q.stats(); got.queued != 0 || got.oldest != 0 || len(g.started) != 0 {
			t.Errorf("superseded work ran or queue did not drain: %+v", got)
		}
	})
}

func TestCostQueueBoundedBurst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, g := newCostQueue(2, 4), newCostQueueGate()
		defer q.shutdown()
		var wg sync.WaitGroup
		admitted := make(chan *costQueueStream, 32)
		for range 32 {
			wg.Go(func() {
				if s := q.register(); s != nil {
					admitted <- s
				}
			})
		}
		wg.Wait()
		close(admitted)
		streams := []*costQueueStream{}
		for s := range admitted {
			streams = append(streams, s)
		}
		if len(streams) != 4 {
			t.Fatalf("registered burst = %d, want cap 4", len(streams))
		}
		for _, s := range streams[:2] {
			q.submit(s, g.request(1))
			g.want(t, 1)
			q.submit(s, g.request(2))
			synctest.Wait()
			if len(g.started) != 0 {
				t.Error("same stream ran concurrently")
			}
		}
		for _, s := range streams {
			for range 100 {
				wg.Go(func() { q.submit(s, g.request(2)) })
			}
		}
		wg.Wait()
		synctest.Wait()
		if got := q.stats(); got.registered != 4 || got.queued != 4 || got.coalesced != 398 || len(q.ready) != 2 || len(g.started) != 0 {
			t.Errorf("burst exceeded worker/per-stream bounds: %+v starts=%d", got, len(g.started))
		}
		q.shutdown()
		if got := q.stats(); got.registered != 0 || got.queued != 0 || got.cancelled != 6 {
			t.Errorf("shutdown did not cancel bounded burst: %+v", got)
		}
	})
}

func TestCostQueueCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, g := newCostQueue(1, 2), newCostQueueGate()
		defer q.shutdown()
		active, queued := q.register(), q.register()
		q.submit(active, g.request(1))
		g.want(t, 1)
		q.submit(queued, g.request(2))
		q.cancel(queued)
		q.cancel(queued)
		if got := q.stats(); got.queued != 0 || got.cancelled != 1 || len(q.ready) != 0 || queued.pending != nil {
			t.Fatalf("queued cancellation retained request: %+v", got)
		}
		q.submit(active, g.request(3))
		q.cancel(active)
		synctest.Wait()
		if !errors.Is(active.ctx.Err(), context.Canceled) || q.stats().registered != 0 || q.stats().cancelled != 3 || len(active.done) != 0 || len(g.started) != 0 {
			t.Error("in-flight cancellation failed to stop work or published completion")
		}
		close(g.release)
		if q.submit(queued, g.request(4)) || q.register() == nil {
			t.Error("cancelled registration accepted work or slot was not reusable")
		}
	})
}

func TestCostQueueMailbox(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newCostQueue(1, 1)
		defer q.shutdown()
		s, failure := q.register(), errors.New("recount failed")
		for version := int64(1); version <= 20; version++ {
			q.submit(s, costQueueRequest{version, func(context.Context) (realtime.Outgoing, error) {
				return realtime.Outgoing{ID: version}, failure
			}})
			synctest.Wait()
			if len(s.done) != 1 || cap(s.done) != 1 {
				t.Errorf("completion mailbox grew or pinned worker: %d", len(s.done))
				break
			}
		}
		if got := <-s.done; got.version != 20 || got.out.ID != 20 || got.err != failure {
			t.Errorf("latest completion/error lost: %+v", got)
		}
		q.submit(s, costQueueRequest{21, func(context.Context) (realtime.Outgoing, error) {
			return realtime.Outgoing{}, nil
		}})
		synctest.Wait()
		q.cancel(s)
		if len(s.done) != 0 {
			t.Error("cancelled registration retained completed mailbox")
		}
	})
}

func TestCostQueueShutdownJoins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newCostQueue(1, 1)
		defer q.shutdown()
		s := q.register()
		cancelled, release, joined := make(chan struct{}), make(chan struct{}), make(chan struct{})
		q.submit(s, costQueueRequest{1, func(ctx context.Context) (realtime.Outgoing, error) {
			select {
			case <-ctx.Done():
				close(cancelled)
			case <-release:
			}
			<-release // Cancellation alone must not free a worker still returning.
			return realtime.Outgoing{}, nil
		}})
		synctest.Wait()
		go func() { q.shutdown(); close(joined) }()
		synctest.Wait()
		select {
		case <-cancelled:
		default:
			t.Error("shutdown did not cancel active context")
		}
		select {
		case <-joined:
			t.Error("shutdown returned before worker exit")
		default:
		}
		if q.register() != nil || q.submit(s, costQueueRequest{}) || q.stats().registered != 1 {
			t.Error("shutdown admitted work or freed a still-running registration")
		}
		close(release)
		<-joined
		synctest.Wait()
		if got := q.stats(); got.registered != 0 || got.cancelled != 1 || len(s.done) != 0 || q.register() != nil {
			t.Errorf("shutdown leaked work/completion: %+v", got)
		}
	})
}
