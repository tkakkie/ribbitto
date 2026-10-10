package middleware

import (
	"encoding/binary"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestMemberRateLimiterBurstAndRefill(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	l := NewMemberRateLimiter(func() time.Time { return now })
	for _, tt := range []struct {
		advance time.Duration
		allowed int
	}{
		{0, 4},
		{time.Second - time.Nanosecond, 0},
		{time.Nanosecond, 1},
		{500 * time.Millisecond, 0},
		{500 * time.Millisecond, 1},
		{10 * time.Second, 4},
	} {
		now = now.Add(tt.advance)
		for range tt.allowed {
			if !l.Allow(kernel.ID{1}, kernel.ID{2}) {
				t.Fatalf("at %s: refused within budget of %d", now, tt.allowed)
			}
		}
		if l.Allow(kernel.ID{1}, kernel.ID{2}) {
			t.Fatalf("at %s: admitted beyond budget of %d", now, tt.allowed)
		}
	}
}

func TestMemberRateLimiterScope(t *testing.T) {
	for _, tt := range []struct {
		name                     string
		organizationID, memberID kernel.ID
	}{
		{"organisation alone differs", kernel.ID{3}, kernel.ID{2}},
		{"member alone differs", kernel.ID{1}, kernel.ID{3}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
			l := NewMemberRateLimiter(func() time.Time { return now })
			for range 4 {
				if !l.Allow(kernel.ID{1}, kernel.ID{2}) {
					t.Fatal("original member refused within burst")
				}
			}
			for range 4 {
				if !l.Allow(tt.organizationID, tt.memberID) {
					t.Fatal("distinct scope shares the exhausted budget")
				}
			}
			if l.Allow(kernel.ID{1}, kernel.ID{2}) || l.Allow(tt.organizationID, tt.memberID) {
				t.Fatal("a scope received more than its burst")
			}
		})
	}
}

func TestMemberRateLimiterSharedBudget(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	l := NewMemberRateLimiter(func() time.Time { return now })
	for i, tt := range []struct{ path, session, address string }{
		{"/channels/a/topics/a", "a", "192.0.2.1:1234"},
		{"/channels/b/topics/a", "a", "192.0.2.1:1234"},
		{"/channels/a/topics/b", "a", "192.0.2.1:1234"},
		{"/channels/a/topics/a", "b", "192.0.2.1:1234"},
		{"/channels/a/topics/a", "a", "192.0.2.2:1234"},
	} {
		r := httptest.NewRequest("POST", tt.path, nil)
		r.Header.Set("Cookie", "session="+tt.session)
		r.RemoteAddr = tt.address
		// The adapter supplies the same trusted IDs for each request. The
		// limiter's API deliberately accepts none of the request metadata.
		if got := l.Allow(kernel.ID{1}, kernel.ID{2}); got != (i < 4) {
			t.Fatalf("%s from %s (%s): allowed = %v", r.URL.Path, r.RemoteAddr, r.Header.Get("Cookie"), got)
		}
	}
	if len(l.buckets) != 1 {
		t.Fatalf("%d buckets, want one shared budget", len(l.buckets))
	}
}

func TestMemberRateLimiterCapacityAndEviction(t *testing.T) {
	t0 := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	now := t0
	l := NewMemberRateLimiter(func() time.Time { return now })
	id := func(n int) kernel.ID {
		var id kernel.ID
		binary.BigEndian.PutUint64(id[:8], uint64(n))
		return id
	}
	for i := 1; i <= 10_000; i++ {
		attempts := 4
		if i == 1 {
			attempts = 1 // This bucket becomes fully replenished first.
		}
		for range attempts {
			if !l.Allow(kernel.ID{1}, id(i)) {
				t.Fatalf("member %d refused within table capacity", i)
			}
		}
	}
	oldKey := memberLimitKey{organizationID: kernel.ID{1}, memberID: id(2)}
	old := l.buckets[oldKey]
	now = t0.Add(time.Second - time.Nanosecond)
	if l.Allow(kernel.ID{1}, id(2)) || l.Allow(kernel.ID{1}, id(10_001)) || len(l.buckets) != 10_000 {
		t.Fatal("empty bucket admitted, partial bucket evicted or full table grew")
	}
	now = t0.Add(time.Second)
	if !l.Allow(kernel.ID{1}, id(10_001)) || len(l.buckets) != 10_000 {
		t.Fatal("fully replenished bucket did not make room for the new key")
	}
	if _, exists := l.buckets[memberLimitKey{organizationID: kernel.ID{1}, memberID: id(1)}]; exists {
		t.Fatal("fully replenished bucket retained during eviction")
	}
	if l.buckets[oldKey] != old || !l.Allow(kernel.ID{1}, id(2)) || l.Allow(kernel.ID{1}, id(2)) {
		t.Fatal("partially replenished member received a fresh burst")
	}
	// Drain the new key too, so no bucket can make room before t0 + 4 s.
	for range 3 {
		if !l.Allow(kernel.ID{1}, id(10_001)) {
			t.Fatal("new member refused within its burst")
		}
	}
	now = t0.Add(4*time.Second - time.Nanosecond)
	if l.Allow(kernel.ID{1}, id(10_002)) || len(l.buckets) != 10_000 {
		t.Fatal("evicted before full refill or created a refused key")
	}
	now = t0.Add(4 * time.Second)
	if !l.Allow(kernel.ID{1}, id(10_002)) {
		t.Fatal("refused requests extended retention past full refill")
	}
	if _, exists := l.buckets[memberLimitKey{organizationID: kernel.ID{1}, memberID: id(3)}]; exists {
		t.Fatal("fully replenished exhausted bucket was not evicted")
	}
	if l.buckets[oldKey] != old || len(l.buckets) > 10_000 {
		t.Fatal("active bucket evicted or capacity exceeded")
	}
}

func TestMemberRateLimiterConcurrentAdmission(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	l := NewMemberRateLimiter(func() time.Time { return now })
	for _, want := range []int32{4, 1} {
		var admitted atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range 100 {
			wg.Go(func() {
				<-start
				if l.Allow(kernel.ID{1}, kernel.ID{2}) {
					admitted.Add(1)
				}
			})
		}
		close(start)
		wg.Wait()
		if got := admitted.Load(); got != want {
			t.Fatalf("concurrent requests admitted = %d, want %d", got, want)
		}
		now = now.Add(time.Second)
	}
}
