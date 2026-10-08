package realtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

var (
	orgA     = kernel.ID{0xa}
	orgB     = kernel.ID{0xb}
	account1 = kernel.ID{1}
	account2 = kernel.ID{2}
	session1 = kernel.ID{0x11}
	session2 = kernel.ID{0x12}
)

// blocked is how long a wait must stay blocked to count as blocked. It is
// short because a wrongly returning Wait returns at once.
const blocked = 20 * time.Millisecond

// waitAsync starts Wait and returns a channel with its result.
func waitAsync(ctx context.Context, h *Hub, org kernel.ID, after int64) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := h.Wait(ctx, org, after)
		done <- err
	}()
	return done
}

// waitForHubWaiters blocks until n calls of Wait for org are blocked, each
// holding the channel the next raise closes.
func waitForHubWaiters(t *testing.T, h *Hub, org kernel.ID, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.Waiting(org) != n {
		if time.Now().After(deadline) {
			t.Fatalf("%d waiters blocked, want %d", h.Waiting(org), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWaitReturnsOnlyAboveCursor(t *testing.T) {
	tests := []struct {
		name    string
		raises  []int64
		after   int64
		returns bool
	}{
		{name: "nothing raised", after: 0, returns: false},
		{name: "latest above cursor", raises: []int64{5}, after: 4, returns: true},
		{name: "latest equals cursor", raises: []int64{5}, after: 5, returns: false},
		{name: "a lower raise does not lower the value", raises: []int64{7, 3}, after: 6, returns: true},
		{name: "a lower raise alone does not wake", raises: []int64{7, 3}, after: 7, returns: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHub()
			for _, seq := range tt.raises {
				h.Raise(orgA, seq)
			}
			ctx, cancel := context.WithTimeout(t.Context(), blocked)
			defer cancel()
			_, err := h.Wait(ctx, orgA, tt.after)
			if tt.returns && err != nil {
				t.Fatalf("Wait = %v, want nil", err)
			}
			if !tt.returns && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Wait = %v, want to block until the deadline", err)
			}
		})
	}
}

// A raise that lands after the connection read its events but before it
// waits must not be lost: the wait sees the level and returns at once.
func TestWaitAfterRaiseBetweenReadAndWait(t *testing.T) {
	h := NewHub()
	cursor := int64(100) // the connection has read up to 100 and found nothing newer
	h.Raise(orgA, 101)   // event 101 commits before the connection waits
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if latest, err := h.Wait(ctx, orgA, cursor); err != nil || latest != 101 {
		t.Fatalf("Wait = %d, %v; want 101, nil", latest, err)
	}
}

func TestRaiseWakesEveryWaiter(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	// Every waiter is joined however the test ends, without reading the
	// results the test itself reads.
	var wg sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	wait := func(org kernel.ID) <-chan error {
		done := make(chan error, 1)
		wg.Go(func() {
			_, err := h.Wait(ctx, org, 0)
			done <- err
		})
		return done
	}
	const waiters = 50
	results := make([]<-chan error, waiters)
	for i := range results {
		results[i] = wait(orgA)
	}
	other := wait(orgB)
	// Every waiter is blocked holding the channel the raise closes, so the
	// raise below is what wakes them, not Wait's immediate return.
	waitForHubWaiters(t, h, orgA, waiters)
	waitForHubWaiters(t, h, orgB, 1)
	h.Raise(orgA, 1)
	for i, done := range results {
		if err := <-done; err != nil {
			t.Fatalf("waiter %d: Wait = %v, want nil", i, err)
		}
	}
	if n := h.Waiting(orgA); n != 0 {
		t.Fatalf("%d waiters of orgA still blocked after the raise", n)
	}
	// The other organisation's waiter is still blocked: it never returned.
	if n := h.Waiting(orgB); n != 1 {
		t.Fatalf("%d waiters of orgB blocked, want 1", n)
	}
	cancel()
	if err := <-other; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter of another organisation: Wait = %v, want it to block until cancelled", err)
	}
}

func TestWaitReportsCause(t *testing.T) {
	h := NewHub()
	cause := errors.New("stream closed")
	ctx, cancel := context.WithCancelCause(t.Context())
	done := waitAsync(ctx, h, orgA, 0)
	cancel(cause)
	if err := <-done; !errors.Is(err, cause) {
		t.Fatalf("Wait = %v, want %v", err, cause)
	}
}

func TestRegisterCancellation(t *testing.T) {
	parentCause := errors.New("request ended")
	tests := []struct {
		name string
		end  func(h *Hub, cancelParent context.CancelCauseFunc, unregister func())
		want error
	}{
		{name: "parent", end: func(_ *Hub, cancelParent context.CancelCauseFunc, _ func()) { cancelParent(parentCause) }, want: parentCause},
		{name: "account", end: func(h *Hub, _ context.CancelCauseFunc, _ func()) { h.CancelAccount(account1) }, want: ErrAccountCancelled},
		{name: "session", end: func(h *Hub, _ context.CancelCauseFunc, _ func()) { h.CancelSession(session1) }, want: ErrSessionEnded},
		{name: "unregister", end: func(_ *Hub, _ context.CancelCauseFunc, unregister func()) { unregister() }, want: ErrUnregistered},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHub()
			parent, cancelParent := context.WithCancelCause(t.Context())
			defer cancelParent(nil)
			ctx, unregister, err := h.Register(parent, Connection{Organization: orgA, Account: account1, Session: session1}, 4)
			if err != nil {
				t.Fatalf("Register = %v", err)
			}
			defer unregister()
			// Connections of another account and session are not touched.
			other, unregisterOther, err := h.Register(t.Context(), Connection{Organization: orgA, Account: account2, Session: session2}, 4)
			if err != nil {
				t.Fatalf("Register other = %v", err)
			}
			defer unregisterOther()

			tt.end(h, cancelParent, unregister)
			<-ctx.Done()
			if got := context.Cause(ctx); !errors.Is(got, tt.want) {
				t.Fatalf("Cause = %v, want %v", got, tt.want)
			}
			if other.Err() != nil {
				t.Fatalf("other connection ended: %v", context.Cause(other))
			}
		})
	}
}

func TestCancelSessionLeavesTheAccountsOtherSessions(t *testing.T) {
	h := NewHub()
	first, unregisterFirst, _ := h.Register(t.Context(), Connection{Organization: orgA, Account: account1, Session: session1}, 4)
	defer unregisterFirst()
	second, unregisterSecond, _ := h.Register(t.Context(), Connection{Organization: orgA, Account: account1, Session: session2}, 4)
	defer unregisterSecond()
	h.CancelSession(session1)
	<-first.Done()
	if second.Err() != nil {
		t.Fatalf("the other session's connection ended: %v", context.Cause(second))
	}
}

func TestSlotHeldUntilUnregister(t *testing.T) {
	h := NewHub()
	c := Connection{Organization: orgA, Account: account1, Session: session1}
	ctx, unregister, err := h.Register(t.Context(), c, 1)
	if err != nil {
		t.Fatalf("Register = %v", err)
	}
	h.CancelSession(session1)
	<-ctx.Done()
	if _, _, err := h.Register(t.Context(), c, 1); !errors.Is(err, ErrTooManyConnections) {
		t.Fatalf("Register after cancel, before unregister = %v, want %v", err, ErrTooManyConnections)
	}
	unregister()
	_, again, err := h.Register(t.Context(), c, 1)
	if err != nil {
		t.Fatalf("Register after unregister = %v", err)
	}
	again()
}

func TestRegisterProcessCap(t *testing.T) {
	h := NewHubWithMaxStreams(1)
	first := Connection{Organization: orgA, Account: account1, Session: session1}
	ctx, unregister, err := h.Register(t.Context(), first, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	// Cancellation alone must not release either slot.
	h.CancelSession(session1)
	<-ctx.Done()
	other := Connection{Organization: orgB, Account: account2, Session: session2}
	ctx, cleanup, err := h.Register(t.Context(), other, 1)
	if !errors.Is(err, ErrTooManyConnections) || ctx != nil || cleanup != nil {
		t.Fatalf("over process cap: context %v, cleanup present %t, error %v", ctx, cleanup != nil, err)
	}
	if len(h.byAccount[account2])+len(h.bySession[session2])+len(h.byOrg[orgB]) != 0 || h.Connections() != 1 {
		t.Fatal("refusal changed the registry or took an account slot")
	}
	unregister()
	unregister() // A repeated cleanup must not release a second process slot.
	_, cleanup, err = h.Register(t.Context(), other, 1)
	if err != nil {
		t.Fatalf("reusing freed slot: %v", err)
	}
	defer cleanup()
	if _, _, err := h.Register(t.Context(), first, 2); !errors.Is(err, ErrTooManyConnections) {
		t.Fatalf("process cap after reusing slot: %v", err)
	}
}

func TestUnregisterIsIdempotent(t *testing.T) {
	h := NewHub()
	ctx, unregister, err := h.Register(t.Context(), Connection{Organization: orgA, Account: account1, Session: session1}, 1)
	if err != nil {
		t.Fatalf("Register = %v", err)
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(unregister)
	}
	wg.Wait()
	unregister()
	h.CancelSession(session1)
	h.CancelAccount(account1)
	// The cause stays ErrUnregistered, though a context keeps its first
	// cause anyway; the empty registry is what shows that Cancel calls can
	// no longer find the connection.
	if got := context.Cause(ctx); !errors.Is(got, ErrUnregistered) {
		t.Fatalf("Cause = %v, want %v", got, ErrUnregistered)
	}
	if n := len(h.byAccount) + len(h.bySession); n != 0 {
		t.Fatalf("registry keeps %d entries after unregister, want 0", n)
	}
}

func TestRegisterLimitUnderConcurrency(t *testing.T) {
	tests := []struct {
		name               string
		processCap, limit  int
		accounts, attempts int
	}{
		{name: "limit reached", processCap: 100, limit: 5, accounts: 1, attempts: 64},
		{name: "limit not reached", processCap: 100, limit: 64, accounts: 1, attempts: 10},
		{name: "limit below one", processCap: 100, limit: 0, accounts: 1, attempts: 3},
		{name: "process cap", processCap: 7, limit: 64, accounts: 2, attempts: 64},
		{name: "both caps", processCap: 7, limit: 5, accounts: 2, attempts: 64},
		{name: "account caps below process cap", processCap: 20, limit: 5, accounts: 2, attempts: 64},
		{name: "default process cap", processCap: DefaultMaxStreams, limit: 6000, accounts: 1, attempts: 5001},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHubWithMaxStreams(tt.processCap)
			if tt.processCap == DefaultMaxStreams {
				h = NewHub()
			}
			var (
				mu          sync.Mutex
				unregisters []func()
				refused     int
				wg          sync.WaitGroup
			)
			start := make(chan struct{})
			for i := range tt.attempts {
				wg.Go(func() {
					<-start
					account := kernel.ID{byte(i%tt.accounts + 1)}
					ctx, unregister, err := h.Register(t.Context(), Connection{Organization: kernel.ID{byte(i%tt.accounts + 10)}, Account: account, Session: kernel.ID{byte(i)}}, tt.limit)
					mu.Lock()
					defer mu.Unlock()
					switch {
					case errors.Is(err, ErrTooManyConnections):
						if ctx != nil || unregister != nil {
							t.Error("refused Register returned a context or unregister function")
						}
						refused++
					case err != nil:
						t.Errorf("Register = %v", err)
					default:
						unregisters = append(unregisters, unregister)
					}
				})
			}
			close(start)
			wg.Wait()
			want := min(tt.processCap, max(tt.limit, 0)*tt.accounts, tt.attempts)
			if len(unregisters) != want || refused != tt.attempts-want {
				t.Fatalf("registered %d and refused %d, want %d and %d", len(unregisters), refused, want, tt.attempts-want)
			}
			if n := h.Connections(); n != want {
				t.Fatalf("Connections() = %d, want %d", n, want)
			}
			registered := 0
			for _, set := range h.byAccount {
				if len(set) > tt.limit {
					t.Fatalf("account holds %d streams, limit %d", len(set), tt.limit)
				}
				registered += len(set)
			}
			if registered != want {
				t.Fatalf("registry holds %d streams, want %d", registered, want)
			}
			for _, unregister := range unregisters {
				unregister()
			}
			if n := h.Connections(); n != 0 {
				t.Fatalf("Connections() after unregister = %d, want 0", n)
			}
			// Both the process and account slots can be reused.
			if _, unregister, err := h.Register(t.Context(), Connection{Organization: orgA, Account: account2, Session: session2}, max(tt.limit, 1)); err != nil {
				t.Fatalf("Register for another account = %v", err)
			} else {
				unregister()
			}
		})
	}
}

func TestRegisterAndUnregisterUnderConcurrency(t *testing.T) {
	h := NewHubWithMaxStreams(4)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 64 {
		wg.Go(func() {
			<-start
			_, unregister, err := h.Register(t.Context(), Connection{Organization: orgA, Account: kernel.ID{byte(i + 1)}, Session: kernel.ID{byte(i + 1)}}, 1)
			if err == nil {
				unregister()
			} else if !errors.Is(err, ErrTooManyConnections) {
				t.Errorf("Register = %v", err)
			}
		})
	}
	close(start)
	wg.Wait()
	if n := h.Connections(); n != 0 {
		t.Fatalf("Connections() after unregister = %d, want 0", n)
	}
}

func TestWaitHonoursAnEndedContextEvenWhenAhead(t *testing.T) {
	h := NewHub()
	h.Raise(orgA, 5)
	cause := errors.New("closed")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	if _, err := h.Wait(ctx, orgA, 1); !errors.Is(err, cause) {
		t.Fatalf("Wait = %v, want %v", err, cause)
	}
}

func TestConnectionsCountsTheRegistry(t *testing.T) {
	h := NewHub()
	_, first, _ := h.Register(t.Context(), Connection{Organization: orgA, Account: account1, Session: session1}, 4)
	_, second, _ := h.Register(t.Context(), Connection{Organization: orgB, Account: account2, Session: session2}, 4)
	if n := h.Connections(); n != 2 {
		t.Fatalf("Connections() = %d, want 2", n)
	}
	// A cancelled connection still counts until it unregisters, like its slot.
	h.CancelSession(session1)
	if n := h.Connections(); n != 2 {
		t.Fatalf("Connections() after cancel = %d, want 2", n)
	}
	first()
	second()
	if n := h.Connections(); n != 0 {
		t.Fatalf("Connections() after unregister = %d, want 0", n)
	}
}

func TestCancelAllEndsEveryConnection(t *testing.T) {
	h := NewHub()
	first, unregisterFirst, _ := h.Register(t.Context(), Connection{Organization: orgA, Account: account1, Session: session1}, 4)
	second, unregisterSecond, _ := h.Register(t.Context(), Connection{Organization: orgB, Account: account2, Session: session2}, 4)
	h.CancelAll()
	for _, ctx := range []context.Context{first, second} {
		if !errors.Is(context.Cause(ctx), ErrShutdown) {
			t.Fatalf("cause %v, want ErrShutdown", context.Cause(ctx))
		}
	}
	// Like the other cancellations, the slots stay until unregister.
	if n := h.Connections(); n != 2 {
		t.Fatalf("Connections() = %d after CancelAll, want 2", n)
	}
	unregisterFirst()
	unregisterSecond()
	if n := h.Connections(); n != 0 {
		t.Fatalf("Connections() = %d after unregister, want 0", n)
	}
}

// A request accepted before shutdown may reach Register after CancelAll ran:
// it is refused, so nothing keeps the server from finishing.
func TestRegisterAfterCancelAllIsRefused(t *testing.T) {
	h := NewHub()
	h.CancelAll()
	ctx, unregister, err := h.Register(t.Context(), Connection{Organization: orgA, Account: account1, Session: session1}, 4)
	if !errors.Is(err, ErrShutdown) || ctx != nil || unregister != nil {
		t.Fatalf("Register after CancelAll = %v, %v; want ErrShutdown and nothing to unregister", ctx, err)
	}
	if n := h.Connections(); n != 0 {
		t.Fatalf("Connections() = %d, want 0", n)
	}
}
