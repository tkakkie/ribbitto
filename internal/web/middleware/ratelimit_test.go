package middleware

import (
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	l := NewRateLimiter(Limit{Burst: 3, Every: 10 * time.Second}, func() time.Time { return now })
	a, b := netip.MustParsePrefix("192.0.2.1/32"), netip.MustParsePrefix("192.0.2.2/32")
	for i := range 3 {
		if !l.Allow(a) {
			t.Fatalf("request %d within the burst refused", i+1)
		}
	}
	if l.Allow(a) {
		t.Fatal("fourth request in the same instant allowed")
	}
	if !l.Allow(b) {
		t.Fatal("another client shares the first one's bucket")
	}
	now = now.Add(10 * time.Second)
	if !l.Allow(a) || l.Allow(a) {
		t.Fatal("refill is not one request per 10 s")
	}
}

func TestRateLimiterBoundedMemory(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	l := NewRateLimiter(Limit{Burst: 2, Every: time.Minute}, func() time.Time { return now })
	l.max = 2
	a, b, c := netip.MustParsePrefix("192.0.2.1/32"), netip.MustParsePrefix("192.0.2.2/32"), netip.MustParsePrefix("192.0.2.3/32")
	l.Allow(a)
	l.Allow(a) // a's bucket is empty now
	l.Allow(b)
	// Full, and nothing has refilled: a new client is refused without a bucket.
	if l.Allow(c) || len(l.buckets) != 2 {
		t.Fatalf("new client allowed or table grew: %d buckets", len(l.buckets))
	}
	// Not yet refilled completely (needs 2 min): still nothing to evict,
	// and a keeps its empty bucket rather than getting a fresh burst.
	now = now.Add(time.Minute + 59*time.Second)
	if l.Allow(c) {
		t.Fatal("evicted a bucket that had not refilled")
	}
	// Refilled: both idle buckets may go, and c gets exactly one burst.
	now = now.Add(time.Second)
	if !l.Allow(c) || !l.Allow(c) || l.Allow(c) {
		t.Fatal("c did not get exactly its own burst after eviction")
	}
	if len(l.buckets) > 2 {
		t.Fatalf("%d buckets, want at most 2", len(l.buckets))
	}
}

func TestClientKey(t *testing.T) {
	trusted, err := ParseTrustedProxies(" 10.0.0.0/8 , 2001:db8:ffff::/48")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{"untrusted peer ignores the header", "198.51.100.7:1234", []string{"203.0.113.9"}, "198.51.100.7/32"},
		{"one trusted hop", "10.0.0.2:1234", []string{"203.0.113.9"}, "203.0.113.9/32"},
		{"trusted multi-hop chain", "10.0.0.2:1234", []string{"203.0.113.9, 10.1.1.1, 10.2.2.2"}, "203.0.113.9/32"},
		{"spoofed leftmost entry", "10.0.0.2:1234", []string{"1.2.3.4, 203.0.113.9"}, "203.0.113.9/32"},
		{"several header lines, in order", "10.0.0.2:1234", []string{"1.2.3.4", "203.0.113.9, 10.1.1.1"}, "203.0.113.9/32"},
		{"missing header", "10.0.0.2:1234", nil, "10.0.0.2/32"},
		{"malformed entry", "10.0.0.2:1234", []string{"203.0.113.9, not-an-ip"}, "10.0.0.2/32"},
		{"only trusted entries", "10.0.0.2:1234", []string{"10.3.3.3"}, "10.0.0.2/32"},
		{"IPv4-mapped IPv6 peer", "[::ffff:198.51.100.7]:1234", nil, "198.51.100.7/32"},
		{"IPv4-mapped IPv6 in the header", "10.0.0.2:1234", []string{"::ffff:203.0.113.9"}, "203.0.113.9/32"},
		{"IPv4-mapped trusted peer", "[::ffff:10.0.0.2]:1234", []string{"203.0.113.9"}, "203.0.113.9/32"},
		{"IPv6 client keyed by /64", "[2001:db8:1:2:3:4:5:6]:1234", nil, "2001:db8:1:2::/64"},
		{"trusted IPv6 proxy", "[2001:db8:ffff::1]:1234", []string{"2001:db8:1:2::9"}, "2001:db8:1:2::/64"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/signin", nil)
			r.RemoteAddr = tt.remote
			for _, value := range tt.xff {
				r.Header.Add("X-Forwarded-For", value)
			}
			if got := ClientKey(r, trusted); got != netip.MustParsePrefix(tt.want) {
				t.Fatalf("ClientKey = %s, want %s", got, tt.want)
			}
		})
	}
	if _, err := ParseTrustedProxies("10.0.0.0/8,nonsense"); err == nil {
		t.Fatal("invalid CIDR accepted")
	}
}
