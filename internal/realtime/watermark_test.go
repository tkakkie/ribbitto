package realtime

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// fakeSequences answers CommittedSequences from a map, records each call's
// organisations, and fails while err is set.
type fakeSequences struct {
	mu    sync.Mutex
	seqs  map[kernel.ID]int64
	err   error
	calls [][]kernel.ID
	done  chan struct{} // receives after every call
	// during, if set, runs inside the read, before it answers; block makes
	// the read wait for its context instead of answering.
	during func()
	block  bool
}

func newFakeSequences() *fakeSequences {
	return &fakeSequences{seqs: map[kernel.ID]int64{}, done: make(chan struct{}, 16)}
}

func (f *fakeSequences) CommittedSequences(ctx context.Context, orgs []kernel.ID) (map[kernel.ID]int64, error) {
	f.mu.Lock()
	during, block := f.during, f.block
	f.mu.Unlock()
	if during != nil {
		during()
	}
	if block {
		<-ctx.Done()
	}
	f.mu.Lock()
	defer func() { f.mu.Unlock(); f.done <- struct{}{} }()
	f.calls = append(f.calls, slices.Clone(orgs))
	if block {
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	out := map[kernel.ID]int64{}
	for _, org := range orgs {
		if seq, ok := f.seqs[org]; ok {
			out[org] = seq
		}
	}
	return out, nil
}

func (f *fakeSequences) set(org kernel.ID, seq int64, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seqs[org] = seq
	f.err = err
}

func (f *fakeSequences) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func TestActiveOrganizationsFollowTheRegistry(t *testing.T) {
	h := NewHub()
	h.Raise(orgB, 3) // a known sequence alone does not make an organisation active
	if got := h.ActiveOrganizations(); len(got) != 0 {
		t.Fatalf("ActiveOrganizations() = %v before any connection, want none", got)
	}
	_, first, _ := h.Register(t.Context(), Connection{Organization: orgA, Account: account1, Session: session1}, 4)
	_, second, _ := h.Register(t.Context(), Connection{Organization: orgA, Account: account2, Session: session2}, 4)
	if got := h.ActiveOrganizations(); !slices.Equal(got, []kernel.ID{orgA}) {
		t.Fatalf("ActiveOrganizations() = %v, want [orgA]", got)
	}
	first()
	if got := h.ActiveOrganizations(); !slices.Equal(got, []kernel.ID{orgA}) {
		t.Fatalf("ActiveOrganizations() = %v with one connection left, want [orgA]", got)
	}
	second()
	if got := h.ActiveOrganizations(); len(got) != 0 {
		t.Fatalf("ActiveOrganizations() = %v after the last unregister, want none", got)
	}
}

func TestWatermarkCheck(t *testing.T) {
	h := NewHub()
	seqs := newFakeSequences()
	w := Watermark{Hub: h, Sequences: seqs}
	if err := w.Check(t.Context()); err != nil || seqs.callCount() != 0 {
		t.Fatalf("Check with no active organisation: %v after %d reads, want no read", err, seqs.callCount())
	}
	_, unregister, _ := h.Register(t.Context(), Connection{Organization: orgA, Account: account1, Session: session1}, 4)
	h.Raise(orgA, 5)
	seqs.set(orgA, 7, nil)
	seqs.set(orgB, 9, nil) // not active: never asked for, never raised
	if err := w.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := h.Latest(orgA); got != 7 {
		t.Fatalf("orgA at %d after the check, want 7", got)
	}
	if got := h.Latest(orgB); got != 0 {
		t.Fatalf("orgB at %d, want 0: it has no connection", got)
	}
	if seqs.calls[len(seqs.calls)-1][0] != orgA || len(seqs.calls[len(seqs.calls)-1]) != 1 {
		t.Fatalf("asked for %v, want only orgA", seqs.calls[len(seqs.calls)-1])
	}
	seqs.set(orgA, 6, nil) // behind the hub: nothing changes
	if err := w.Check(t.Context()); err != nil || h.Latest(orgA) != 7 {
		t.Fatalf("Check with the database behind: %v, hub at %d; want 7", err, h.Latest(orgA))
	}
	failure := errors.New("database unavailable")
	seqs.set(orgA, 8, failure)
	if err := w.Check(t.Context()); !errors.Is(err, failure) || h.Latest(orgA) != 7 {
		t.Fatalf("failed Check: %v, hub at %d; want the error and 7", err, h.Latest(orgA))
	}
	unregister()
	calls := seqs.callCount()
	if err := w.Check(t.Context()); err != nil || seqs.callCount() != calls {
		t.Fatalf("Check after the last unregister read %d times, want none", seqs.callCount()-calls)
	}
}

// Several active organisations are read in one query.
func TestWatermarkReadsActiveOrganisationsTogether(t *testing.T) {
	h := NewHub()
	seqs := newFakeSequences()
	seqs.set(orgA, 4, nil)
	seqs.set(orgB, 6, nil)
	_, a, _ := h.Register(t.Context(), Connection{Organization: orgA, Account: account1, Session: session1}, 4)
	defer a()
	_, b, _ := h.Register(t.Context(), Connection{Organization: orgB, Account: account2, Session: session2}, 4)
	defer b()
	if err := (Watermark{Hub: h, Sequences: seqs}).Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := seqs.callCount(); n != 1 || len(seqs.calls[0]) != 2 {
		t.Fatalf("%d reads of %v, want one read of both organisations", n, seqs.calls)
	}
	if h.Latest(orgA) != 4 || h.Latest(orgB) != 6 {
		t.Fatalf("hub at %d and %d, want 4 and 6", h.Latest(orgA), h.Latest(orgB))
	}
}

// An organisation whose last connection goes away while its sequence is
// being read is not raised.
func TestWatermarkSkipsAnOrganisationThatWentInactive(t *testing.T) {
	h := NewHub()
	seqs := newFakeSequences()
	seqs.set(orgA, 9, nil)
	_, unregister, _ := h.Register(t.Context(), Connection{Organization: orgA, Account: account1, Session: session1}, 4)
	seqs.during = unregister // the last connection leaves during the read
	if err := (Watermark{Hub: h, Sequences: seqs}).Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := h.Latest(orgA); got != 0 {
		t.Fatalf("hub at %d, want 0: the organisation went inactive during the read", got)
	}
}

// A stalled read ends at the check's timeout, and the next check works.
func TestWatermarkCheckTimesOut(t *testing.T) {
	h := NewHub()
	seqs := newFakeSequences()
	seqs.set(orgA, 3, nil)
	seqs.block = true
	_, unregister, _ := h.Register(t.Context(), Connection{Organization: orgA, Account: account1, Session: session1}, 4)
	defer unregister()
	w := Watermark{Hub: h, Sequences: seqs, Timeout: 20 * time.Millisecond}
	if err := w.Check(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled Check = %v, want its timeout", err)
	}
	seqs.mu.Lock()
	seqs.block = false
	seqs.mu.Unlock()
	if err := w.Check(t.Context()); err != nil || h.Latest(orgA) != 3 {
		t.Fatalf("Check after a timeout: %v, hub at %d; want 3", err, h.Latest(orgA))
	}
}

// An event committed without a Raise reaches a stream that has caught up
// after the next successful tick, and a failed tick is retried on the next.
func TestWatermarkDeliversAnUnannouncedCommit(t *testing.T) {
	hub := NewHub()
	log := &fakeLog{events: []Event{posted(1, channelA)}}
	seqs := newFakeSequences()
	seqs.set(orgA, 1, nil)
	hub.Raise(orgA, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	streamCtx, unregister, err := hub.Register(ctx, Connection{Organization: orgA, Account: account1, Session: session1}, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	send := newRecorder()
	done := runAsync(streamCtx, Stream{Hub: hub, Events: log, Authorizer: authorizerFunc(allowAll), Renderer: rendererFunc(render)}, 0, send)
	t.Cleanup(func() {
		cancel()
		<-done
	})
	send.waitFor(t, 1)
	// Caught up and blocked in the hub: only a raise can wake the stream now,
	// so the delivery below proves the watermark's raise, not a read the
	// stream made before it waited.
	waitForHubWaiters(t, hub, orgA, 1)

	ticks := make(chan time.Time)
	var worker sync.WaitGroup
	worker.Go(func() { Watermark{Hub: hub, Sequences: seqs}.Run(ctx, ticks) })
	t.Cleanup(func() {
		cancel()
		worker.Wait()
	})
	tick := func() {
		t.Helper()
		select {
		case ticks <- time.Now():
		case <-time.After(5 * time.Second):
			t.Fatal("the watermark did not take the tick")
		}
		select {
		case <-seqs.done:
		case <-time.After(5 * time.Second):
			t.Fatal("the watermark did not read")
		}
	}

	log.append(posted(2, channelA)) // committed; nothing raises the hub
	seqs.set(orgA, 2, errors.New("database unavailable"))
	tick()
	if got := hub.Latest(orgA); got != 1 {
		t.Fatalf("hub at %d after a failed tick, want 1", got)
	}
	seqs.set(orgA, 2, nil)
	tick()
	send.waitFor(t, 1, 2)
}
