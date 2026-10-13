package realtime_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tkakkie/ribbitto/internal/realtime"
)

func TestRecountSchedulerStatsLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := realtime.NewRecountScheduler(2, 3)
		t.Cleanup(q.Close)
		want := realtime.RecountStats{WorkerLimit: 2, RegistrationLimit: 3, WaitingWorkers: 2}
		checkRecountStats(t, q, want)
		a, b, c := q.Register(), q.Register(), q.Register()
		want.Registrations = 3
		checkRecountStats(t, q, want)
		if q.Register() != nil {
			t.Fatal("full scheduler accepted registration")
		}
		want.RefusedCapacity++
		starts := make(chan int, 3)
		first, second := schedulerGate(t, starts, 1), schedulerGate(t, starts, 2)
		a.Submit(1, first.compute)
		schedulerStarts(t, starts, 1)
		want.RunningWorkers, want.WaitingWorkers = 1, 1
		checkRecountStats(t, q, want)
		<-time.NewTimer(2 * time.Millisecond).C
		b.Submit(2, second.compute)
		schedulerStarts(t, starts, 2)
		want.RunningWorkers, want.WaitingWorkers = 2, 0
		<-time.NewTimer(3 * time.Millisecond).C
		a.Submit(3, first.compute)
		want.Pending, want.OldestRunningAge = 1, 5*time.Millisecond
		checkRecountStats(t, q, want) // A successor is pending but ineligible.
		<-time.NewTimer(2 * time.Millisecond).C
		a.Submit(4, first.compute)
		c.Submit(5, second.compute)
		<-time.NewTimer(3 * time.Millisecond).C
		c.Submit(6, second.compute)
		want.Pending, want.Eligible = 2, 1
		want.OldestPendingAge, want.OldestRunningAge = 5*time.Millisecond, 10*time.Millisecond
		checkRecountStats(t, q, want) // Both replacements preserve continuous age.
		a.Cancel()
		a.Cancel()
		want.Pending, want.OldestPendingAge, want.Cancelled = 1, 3*time.Millisecond, 2
		checkRecountStats(t, q, want) // The queued replacement also retains its age.
		c.Cancel()
		c.Cancel()
		want.Pending, want.Eligible, want.Registrations, want.Cancelled = 0, 0, 2, 3
		want.OldestPendingAge = 0
		checkRecountStats(t, q, want) // Cancellation frees neither occupied worker.
		if a.Submit(7, first.compute) || b.Submit(8, nil) {
			t.Fatal("cancelled or nil computation accepted")
		}
		want.RejectedSubmissions = 2
		go q.Close()
		synctest.Wait()
		want.Closed, want.Cancelled = true, 4
		if q.Register() != nil || b.Submit(9, second.compute) {
			t.Fatal("closed scheduler accepted work")
		}
		want.RefusedClosed, want.RejectedSubmissions = 1, 3
		checkRecountStats(t, q, want)
		<-time.NewTimer(time.Millisecond).C
		want.OldestRunningAge = 11 * time.Millisecond
		checkRecountStats(t, q, want) // Unfinished work remains visible after Close.
		first.open()
		synctest.Wait()
		want.RunningWorkers, want.Registrations, want.Completed, want.ErrorsAfterCancellation = 1, 1, 1, 1
		want.ProcessingDuration, want.OldestRunningAge = 11*time.Millisecond, 9*time.Millisecond
		want.DurationBuckets[2] = 1
		checkRecountStats(t, q, want)
		second.open()
		synctest.Wait()
		q.Close()
		want.RunningWorkers, want.Registrations, want.OldestRunningAge = 0, 0, 0
		want.Completed, want.ErrorsAfterCancellation, want.ProcessingDuration = 2, 2, 20*time.Millisecond
		want.DurationBuckets[1] = 1
		checkRecountStats(t, q, want)
	})
}

func TestRecountSchedulerStatsProcessing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := realtime.NewRecountScheduler(1, 1)
		t.Cleanup(q.Close)
		r := q.Register()
		want := realtime.RecountStats{WorkerLimit: 1, RegistrationLimit: 1, Registrations: 1, WaitingWorkers: 1}
		for i, tc := range []struct {
			duration time.Duration
			bucket   int
		}{
			{0, 0}, {time.Millisecond, 0}, {time.Millisecond + 1, 1},
			{10 * time.Millisecond, 1}, {10*time.Millisecond + 1, 2},
			{100 * time.Millisecond, 2}, {100*time.Millisecond + 1, 3},
			{time.Second, 3}, {time.Second + 1, 4},
			{10 * time.Second, 4}, {10*time.Second + 1, 5},
		} {
			gate := make(chan struct{})
			r.Submit(uint64(i), func(context.Context) (realtime.Outgoing, error) {
				<-gate
				if i == 0 {
					return realtime.Outgoing{}, errors.New("failed")
				}
				if i == 1 {
					panic("failed")
				}
				return realtime.Outgoing{}, nil
			})
			synctest.Wait()
			<-time.NewTimer(tc.duration).C
			close(gate)
			synctest.Wait()
			want.Completed++
			want.ProcessingDuration += tc.duration
			want.DurationBuckets[tc.bucket]++
			if i < 2 {
				want.Errors++
			}
			checkRecountStats(t, q, want) // Unread/replaced mailboxes still count returns.
		}
		copy := q.Stats()
		copy.DurationBuckets[0] = 999
		checkRecountStats(t, q, want)
		if n := testing.AllocsPerRun(10, func() { _ = q.Stats() }); n != 0 {
			t.Fatalf("Stats allocated %g times, want 0", n)
		}
		r.Cancel() // A completed, buffered result is not a cancelled computation.
		want.Registrations = 0
		checkRecountStats(t, q, want)
		q.Close()
		want.Closed, want.WaitingWorkers = true, 0
		checkRecountStats(t, q, want)
	})
}

func TestRecountSchedulerStatsErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cancelled bool
		panic     bool
		err       error
	}{
		{name: "live error", err: errors.New("failed")},
		{name: "live panic", panic: true},
		{name: "live context cancellation", err: context.Canceled},
		{name: "cancelled context", cancelled: true, err: context.Canceled},
		{name: "cancelled error", cancelled: true, err: errors.New("failed")},
		{name: "cancelled panic", cancelled: true, panic: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				q := realtime.NewRecountScheduler(1, 1)
				defer q.Close()
				r := q.Register()
				gate := make(chan struct{})
				defer close(gate)
				r.Submit(1, func(context.Context) (realtime.Outgoing, error) {
					<-gate
					if tc.panic {
						panic("failed")
					}
					return realtime.Outgoing{}, tc.err
				})
				synctest.Wait()
				if tc.cancelled {
					r.Cancel()
				}
				gate <- struct{}{}
				synctest.Wait()
				live, cancelled := uint64(1), uint64(0)
				if tc.cancelled {
					live, cancelled = 0, 1
				}
				check := func() {
					t.Helper()
					s := q.Stats()
					if s.Completed != 1 || s.Errors != live || s.ErrorsAfterCancellation != cancelled {
						t.Fatalf("Completed=%d Errors=%d ErrorsAfterCancellation=%d; want 1 %d %d", s.Completed, s.Errors, s.ErrorsAfterCancellation, live, cancelled)
					}
				}
				check()
				r.Cancel() // Cancellation after return processing cannot reclassify it.
				check()
			})
		})
	}
}

func TestRecountSchedulerStatsClosedRefusalPrecedesCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := realtime.NewRecountScheduler(1, 1)
		t.Cleanup(q.Close)
		r := q.Register()
		gate := schedulerGate(t, make(chan int, 1), 1)
		r.Submit(1, gate.compute)
		synctest.Wait()
		go q.Close()
		synctest.Wait()
		if q.Register() != nil {
			t.Fatal("closed full scheduler accepted registration")
		}
		checkRecountStats(t, q, realtime.RecountStats{
			WorkerLimit: 1, RegistrationLimit: 1, Registrations: 1,
			Closed: true, RunningWorkers: 1, Cancelled: 1, RefusedClosed: 1,
		})
	})
}

func checkRecountStats(t *testing.T, q *realtime.RecountScheduler, want realtime.RecountStats) {
	t.Helper()
	synctest.Wait()
	want.SnapshotAt = time.Now()
	if got := q.Stats(); got != want {
		t.Fatalf("Stats = %+v; want %+v", got, want)
	}
}
