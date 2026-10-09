package realtime

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
)

func TestHubSnapshotRaiseBetweenReadAndWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewHub()
		read, resume := make(chan struct{}), make(chan struct{})
		h.afterWaitRead = sync.OnceFunc(func() { close(read); <-resume })
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan result, 1)
		go func() { seq, err := h.Wait(ctx, orgA, 0); done <- result{seq, err} }()
		<-read // the waiter owns the old level/channel, but has not selected
		h.Raise(orgA, 1)
		close(resume)
		synctest.Wait()
		select {
		case got := <-done:
			if got.cursor != 1 || got.err != nil {
				t.Errorf("Wait = %+v, want 1 and nil", got)
			}
		default:
			t.Error("raise between snapshot read and wait was lost")
		}
		cancel()
		synctest.Wait()
		if h.Waiting(orgA) != 0 {
			t.Error("waiter accounting leaked")
		}
	})
}

func TestHubSnapshotCompetingRaises(t *testing.T) {
	for _, delayed := range []int64{1, 3} {
		synctest.Test(t, func(t *testing.T) {
			h := NewHub()
			read, resume := make(chan struct{}), make(chan struct{})
			gate := sync.OnceFunc(func() { close(read); <-resume })
			h.beforeRaise = func(seq int64) {
				if seq == delayed {
					gate()
				}
			}
			done := make(chan struct{})
			go func() { h.Raise(orgA, delayed); close(done) }()
			<-read // delayed raise has read level 0
			h.Raise(orgA, 2)
			if got := h.Latest(orgA); got != 2 {
				t.Fatalf("Latest = %d, want 2", got)
			}
			close(resume)
			<-done
			if got := h.Latest(orgA); got != max(2, delayed) {
				t.Errorf("Latest = %d after competing raises, want %d", got, max(2, delayed))
			}
		})
	}
}

func TestHubSnapshotReadersIgnoreRegistryLock(t *testing.T) {
	h := NewHub()
	h.Raise(orgA, 1)
	h.mu.Lock()
	done := make(chan result, 1)
	go func() {
		seq, err := h.Wait(t.Context(), orgA, 0)
		if latest := h.Latest(orgA); latest != seq {
			t.Errorf("Latest = %d, Wait = %d", latest, seq)
		}
		done <- result{seq, err}
	}()
	// The timeout only bounds a broken implementation; no sleep orders work.
	got := func() result { defer h.mu.Unlock(); return receive(t, done) }()
	if got.cursor != 1 || got.err != nil {
		t.Fatalf("Wait = %+v, want 1 and nil", got)
	}
}

func TestHubSnapshotConcurrentEntryCreation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewHub()
		ready, resume := make(chan struct{}, 2), make(chan struct{})
		h.beforeSequenceStore = func() { ready <- struct{}{}; <-resume }
		entries := make(chan *orgSequence, 2)
		for range 2 {
			go func() { entries <- h.sequence(orgA) }()
		}
		<-ready
		<-ready // both callers missed the map and have allocated an entry
		close(resume)
		a, b := <-entries, <-entries
		if a != b {
			t.Error("concurrent callers got different organisation entries")
		}
		h.Raise(orgA, 1)
		for _, entry := range []*orgSequence{a, b} {
			if entry.state.Load().latest != 1 {
				t.Error("raise did not reach every caller's entry")
			}
		}
	})
}
