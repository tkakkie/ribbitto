package realtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/domain"
)

var (
	orgA     = domain.ID{0xa}
	orgB     = domain.ID{0xb}
	account1 = domain.ID{1}
	account2 = domain.ID{2}
	session1 = domain.ID{0x11}
	session2 = domain.ID{0x12}
)

// blocked is how long a wait must stay blocked to count as blocked. It is
// short because a wrongly returning Wait returns at once.
const blocked = 20 * time.Millisecond

// waitAsync starts Wait and returns a channel with its result.
func waitAsync(ctx context.Context, h *Hub, org domain.ID, after int64) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := h.Wait(ctx, org, after)
		done <- err
	}()
	return done
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
	defer cancel()
	const waiters = 50
	results := make([]<-chan error, waiters)
	for i := range results {
		results[i] = waitAsync(ctx, h, orgA, 0)
	}
	other := waitAsync(ctx, h, orgB, 0)
	// Give the waiters time to reach their blocking select; none may have
	// returned yet, so the raise below is what wakes them.
	time.Sleep(blocked)
	for i, done := range results {
		select {
		case err := <-done:
			t.Fatalf("waiter %d returned %v before the raise", i, err)
		default:
		}
	}
	h.Raise(orgA, 1)
	for i, done := range results {
		if err := <-done; err != nil {
			t.Fatalf("waiter %d: Wait = %v, want nil", i, err)
		}
	}
	select {
	case err := <-other:
		t.Fatalf("waiter of another organisation returned %v", err)
	case <-time.After(blocked):
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
		name     string
		limit    int
		attempts int
	}{
		{name: "limit reached", limit: 5, attempts: 64},
		{name: "limit not reached", limit: 64, attempts: 10},
		{name: "limit below one", limit: 0, attempts: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHub()
			var (
				mu          sync.Mutex
				unregisters []func()
				refused     int
				wg          sync.WaitGroup
			)
			start := make(chan struct{})
			for range tt.attempts {
				wg.Go(func() {
					<-start
					_, unregister, err := h.Register(t.Context(), Connection{Organization: orgA, Account: account1, Session: session1}, tt.limit)
					mu.Lock()
					defer mu.Unlock()
					switch {
					case errors.Is(err, ErrTooManyConnections):
						if unregister != nil {
							t.Error("refused Register returned an unregister function")
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
			want := min(max(tt.limit, 0), tt.attempts)
			if len(unregisters) != want || refused != tt.attempts-want {
				t.Fatalf("registered %d and refused %d, want %d and %d", len(unregisters), refused, want, tt.attempts-want)
			}
			for _, unregister := range unregisters {
				unregister()
			}
			// Another account has its own count.
			if _, unregister, err := h.Register(t.Context(), Connection{Organization: orgA, Account: account2, Session: session2}, max(tt.limit, 1)); err != nil {
				t.Fatalf("Register for another account = %v", err)
			} else {
				unregister()
			}
		})
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
