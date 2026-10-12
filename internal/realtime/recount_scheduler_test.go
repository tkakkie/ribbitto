package realtime_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/tkakkie/ribbitto/internal/realtime"
)

type recountGate struct {
	open    func()
	compute func(context.Context) (realtime.Outgoing, error)
}

func schedulerGate(t *testing.T, starts chan int, id int) recountGate {
	t.Helper()
	gate := make(chan struct{})
	open := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(open)
	return recountGate{open, func(context.Context) (realtime.Outgoing, error) {
		starts <- id
		<-gate
		return realtime.Outgoing{Data: []byte("computed")}, errors.New("computation error")
	}}
}

func schedulerStarts(t *testing.T, starts chan int, want ...int) {
	t.Helper()
	synctest.Wait()
	if len(starts) != len(want) {
		t.Fatalf("started count = %d, want %d", len(starts), len(want))
	}
	for _, id := range want {
		if got := <-starts; got != id {
			t.Fatalf("started = %d, want %d (FIFO eligibility)", got, id)
		}
	}
}

func schedulerClosedMailbox(t *testing.T, r *realtime.RecountRegistration) {
	t.Helper()
	select {
	case result, ok := <-r.Completions():
		if ok {
			t.Fatalf("cancelled mailbox delivered %+v, want ok == false", result)
		}
	default:
		t.Fatal("cancelled mailbox is still open, want receive with ok == false")
	}
}

func TestRecountSchedulerFIFOAndMailbox(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := realtime.NewRecountScheduler(1, 3)
		t.Cleanup(q.Close)
		a, b, c := q.Register(), q.Register(), q.Register()
		if a == nil || b == nil || c == nil || q.Register() != nil {
			t.Fatal("registration limit must admit exactly three streams")
		}
		starts := make(chan int, 10)
		first, second, third, successor := schedulerGate(t, starts, 1), schedulerGate(t, starts, 6), schedulerGate(t, starts, 5), schedulerGate(t, starts, 3)
		a.Submit(1, first.compute)
		schedulerStarts(t, starts, 1)
		a.Submit(2, schedulerGate(t, starts, 2).compute)
		a.Submit(3, successor.compute)
		b.Submit(4, schedulerGate(t, starts, 4).compute)
		c.Submit(5, third.compute)
		b.Submit(6, second.compute)
		schedulerStarts(t, starts) // The single worker is still occupied.
		first.open()
		schedulerStarts(t, starts, 6)
		second.open()
		schedulerStarts(t, starts, 5)
		third.open()
		schedulerStarts(t, starts, 3)
		successor.open()
		schedulerStarts(t, starts)
		// Leave a's first completion unread; another stream must still progress.
		last := schedulerGate(t, starts, 7)
		b.Submit(7, last.compute)
		schedulerStarts(t, starts, 7)
		last.open()
		schedulerStarts(t, starts)
		if len(a.Completions()) != 1 {
			t.Fatalf("mailbox size = %d, want 1", len(a.Completions()))
		}
		result := <-a.Completions()
		if result.Version != 3 || string(result.Outgoing.Data) != "computed" || result.Err == nil {
			t.Fatalf("completion = %+v, want latest version 3 with output and error", result)
		}
		b.Cancel() // Its unread completion must be discarded before closure.
		schedulerClosedMailbox(t, b)
	})
}

func TestRecountSchedulerWorkerAndStreamLimits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := realtime.NewRecountScheduler(2, 3)
		t.Cleanup(q.Close)
		a, b, c := q.Register(), q.Register(), q.Register()
		starts := make(chan int, 10)
		first, second, third := schedulerGate(t, starts, 1), schedulerGate(t, starts, 2), schedulerGate(t, starts, 3)
		a.Submit(1, first.compute)
		schedulerStarts(t, starts, 1)
		a.Submit(9, schedulerGate(t, starts, 9).compute)
		schedulerStarts(t, starts) // A free worker cannot run a's successor.
		b.Submit(2, second.compute)
		schedulerStarts(t, starts, 2)
		c.Submit(3, third.compute)
		schedulerStarts(t, starts) // Both workers are occupied.
		second.open()
		schedulerStarts(t, starts, 3)
	})
}

func TestRecountSchedulerCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := realtime.NewRecountScheduler(1, 1)
		t.Cleanup(q.Close)
		r := q.Register()
		gate := schedulerGate(t, make(chan int, 1), 1)
		var computeCtx context.Context
		r.Submit(1, func(ctx context.Context) (realtime.Outgoing, error) {
			computeCtx = ctx
			return gate.compute(ctx)
		})
		synctest.Wait()
		r.Cancel()
		r.Cancel()
		synctest.Wait()
		if computeCtx.Err() != context.Canceled {
			t.Fatal("Cancel must cancel computation context")
		}
		schedulerClosedMailbox(t, r)
		if q.Register() != nil {
			t.Fatal("cancelled computation must retain registration capacity until return")
		}
		gate.open()
		synctest.Wait()
		schedulerClosedMailbox(t, r)
		if r.Submit(2, func(context.Context) (realtime.Outgoing, error) { return realtime.Outgoing{}, nil }) {
			t.Fatal("cancelled registration accepted work")
		}
		if q.Register() == nil {
			t.Fatal("returned computation must release registration capacity")
		}
		q.Close()
		if q.Register() != nil {
			t.Fatal("closed scheduler accepted registration")
		}
	})
}

func TestRecountSchedulerCloseWaitsForComputation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := realtime.NewRecountScheduler(1, 1)
		t.Cleanup(q.Close)
		r := q.Register()
		starts := make(chan int, 1)
		gate := schedulerGate(t, starts, 1)
		var computeCtx context.Context
		r.Submit(1, func(ctx context.Context) (realtime.Outgoing, error) {
			computeCtx = ctx
			return gate.compute(ctx) // Ignores cancellation until explicitly released.
		})
		schedulerStarts(t, starts, 1)
		if q.Register() != nil {
			t.Fatal("blocked computation must occupy registration capacity")
		}
		closed := make(chan struct{})
		go func() {
			q.Close()
			close(closed)
		}()
		synctest.Wait()
		if computeCtx.Err() != context.Canceled {
			t.Fatal("Close must cancel computation context")
		}
		select {
		case <-closed:
			t.Fatal("Close returned before blocked computation returned")
		default:
		}
		if q.Register() != nil {
			t.Fatal("closing scheduler accepted registration while computation was blocked")
		}
		schedulerClosedMailbox(t, r)
		gate.open()
		synctest.Wait()
		select {
		case <-closed:
		default:
			t.Fatal("Close did not return after computation returned")
		}
		schedulerClosedMailbox(t, r)
		if q.Register() != nil {
			t.Fatal("closed scheduler accepted registration")
		}
	})
}
