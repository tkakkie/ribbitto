package middleware

import (
	"fmt"
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
	for _, invalid := range []string{"10.0.0.0/8,nonsense", "::ffff:0:0/95"} {
		if _, err := ParseTrustedProxies(invalid); err == nil {
			t.Fatalf("invalid trusted proxies %q accepted", invalid)
		}
	}
	// An IPv4-mapped CIDR means the same as its IPv4 form, for peers and
	// for hops in the header, however either is spelled.
	mapped, err := ParseTrustedProxies("::ffff:10.0.0.0/104")
	if err != nil || len(mapped) != 1 || mapped[0] != netip.MustParsePrefix("10.0.0.0/8") {
		t.Fatalf("mapped CIDR parsed as %v, %v", mapped, err)
	}
	for _, remote := range []string{"10.0.0.2:1234", "[::ffff:10.0.0.2]:1234"} {
		r := httptest.NewRequest("POST", "/signin", nil)
		r.RemoteAddr = remote
		r.Header.Set("X-Forwarded-For", "203.0.113.9, ::ffff:10.9.9.9")
		if got := ClientKey(r, mapped); got != netip.MustParsePrefix("203.0.113.9/32") {
			t.Fatalf("peer %s with a mapped trusted CIDR: key %s", remote, got)
		}
	}
}

// sixtyFour is the i-th /64 in 2001:db8::/48.
func sixtyFour(i int) netip.Prefix {
	a := netip.MustParseAddr("2001:db8::").As16()
	a[6], a[7] = byte(i>>8), byte(i)
	return netip.PrefixFrom(netip.AddrFrom16(a), 64)
}

// One /48 must not fill the sign-in table by spreading over its /64s and
// re-probing them to keep them alive.
func TestSignInNetworkCannotFillTable(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	l := NewAuthLimits(nil, func() time.Time { return now }).SignIn
	for range 2 {
		for i := range maxBuckets {
			l.Allow(sixtyFour(i))
		}
		now = now.Add(30 * time.Second)
	}
	if !l.Allow(netip.MustParsePrefix("198.51.100.7/32")) {
		t.Fatal("one /48 turned a new client away")
	}
}

// The /64s of one /48 together get the /48's budget, not one each.
func TestSignInNetworkBudget(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	l := NewAuthLimits(nil, func() time.Time { return now }).SignIn
	admitted := 0
	for i := range 3600 {
		if l.Allow(sixtyFour(i)) {
			admitted++
		}
		now = now.Add(time.Second)
	}
	// A burst of 10, then one every 6 s: 10 + 599 within 3,599 s.
	if admitted != 609 {
		t.Fatalf("%d sign-ins admitted from one /48 in an hour, want 609", admitted)
	}
}

// A request refused by one bucket takes nothing from the other, creates
// no bucket and does not extend any bucket's retention.
func TestRefusedRequestTakesNothing(t *testing.T) {
	t0 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	now := t0
	l := NewAuthLimits(nil, func() time.Time { return now }).SignIn
	a := sixtyFour(0)
	l.Allow(a) // a: 4 tokens left
	for i := 1; i < 10; i++ {
		if !l.Allow(sixtyFour(i)) {
			t.Fatalf("/64 %d refused within the /48 burst", i)
		}
	}
	// The /48 is empty: a is refused although its own bucket has tokens.
	now = t0.Add(time.Second)
	if l.Allow(a) {
		t.Fatal("admitted past an empty /48")
	}
	if got := l.buckets[a].limiter.TokensAt(now); got < 4 || got >= 5 {
		t.Fatalf("the /48's refusal took a's token: %v left", got)
	}
	if l.buckets[a].last != t0 {
		t.Fatal("a refused request moved last")
	}
	if l.Allow(sixtyFour(10)) || len(l.buckets) != 10 {
		t.Fatalf("a refused /64 got a bucket: %d buckets", len(l.buckets))
	}
	// A /64 bucket refusing takes no /48 token: drain a, then let the /48
	// earn exactly one token, which another /64 must still get.
	for l.buckets[a].limiter.TokensAt(now) >= 1 {
		l.buckets[a].limiter.AllowN(now, 1)
	}
	now = t0.Add(6 * time.Second)
	if l.Allow(a) {
		t.Fatal("a admitted with an empty bucket")
	}
	if !l.Allow(sixtyFour(11)) {
		t.Fatal("a's refused request took the /48's token")
	}
}

// Churn: a /48 tries a new /64 at every step, then hammers every /64 it
// has had admitted. Over the run it gets more distinct /64s admitted than
// the table holds, so buckets must be evicted, yet it never keeps more than
// the documented number unevictable, and a client from elsewhere still gets
// in.
func TestNetworkChurn(t *testing.T) {
	for _, tt := range []struct {
		name  string
		pick  func(*AuthLimits) *RateLimiter
		limit Limit
		step  time.Duration
		bound int
	}{
		{"sign-in", func(a *AuthLimits) *RateLimiter { return a.SignIn }, Limit{signInBurst, signInEvery}, time.Second, 20},
		{"sign-up", func(a *AuthLimits) *RateLimiter { return a.SignUp }, Limit{onceBurst, onceEvery}, 30 * time.Second, 12},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
			l := tt.pick(NewAuthLimits(nil, func() time.Time { return now }))
			l.max = 40
			window := time.Duration(tt.limit.Burst) * tt.limit.Every
			var admitted []netip.Prefix
			for step := range 600 {
				if fresh := sixtyFour(step); l.Allow(fresh) {
					admitted = append(admitted, fresh)
				}
				for range 3 {
					for _, old := range admitted {
						l.Allow(old)
					}
				}
				unevictable := 0
				for _, b := range l.buckets {
					if now.Sub(b.last) < window {
						unevictable++
					}
				}
				if unevictable > tt.bound {
					t.Fatalf("step %d: %d unevictable buckets from one /48, want at most %d", step, unevictable, tt.bound)
				}
				now = now.Add(tt.step)
			}
			if len(admitted) <= l.max {
				t.Fatalf("only %d distinct /64s admitted; the churn never outgrew the %d-entry table", len(admitted), l.max)
			}
			if !l.Allow(netip.MustParsePrefix("198.51.100.7/32")) {
				t.Fatal("a client from elsewhere was turned away after the churn")
			}
		})
	}
}

// The /48 table follows the client table's rules: a refused request does
// not move a /48's last, a full /48 table refuses a new /48 without
// creating a client bucket, and a /48 bucket is evicted only once it has
// refilled, after which the new /48 gets exactly its own burst.
func TestNetworkTable(t *testing.T) {
	t0 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	now := t0
	l := NewNetworkRateLimiter(Limit{Burst: 5, Every: 12 * time.Second}, Limit{Burst: 2, Every: time.Minute}, func() time.Time { return now })
	l.max = 2
	x := netip.MustParsePrefix("2001:db8:1::/64")
	y := netip.MustParsePrefix("2001:db8:2::/64")
	xNet := netip.MustParsePrefix("2001:db8:1::/48")
	if !l.Allow(x) || !l.Allow(y) || !l.Allow(x) {
		t.Fatal("refused within the bursts")
	}
	now = t0.Add(30 * time.Second)
	if l.Allow(x) {
		t.Fatal("admitted past an empty /48")
	}
	if l.networks[xNet].last != t0 {
		t.Fatal("a refused request moved the /48's last")
	}
	// Client buckets are evictable after 60 s, /48 buckets after 120 s.
	z := func(i int) netip.Prefix {
		return netip.PrefixFrom(netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 3, 0, byte(i)}), 64)
	}
	now = t0.Add(119 * time.Second)
	if l.Allow(z(0)) || len(l.networks) != 2 || len(l.buckets) != 0 {
		t.Fatalf("full /48 table: admitted, or %d /48 buckets and %d client buckets", len(l.networks), len(l.buckets))
	}
	now = t0.Add(120 * time.Second)
	// z(0)'s own bucket still has tokens: only the /48 can refuse it.
	if !l.Allow(z(0)) || !l.Allow(z(1)) || l.Allow(z(0)) {
		t.Fatal("the new /48 did not get exactly its own burst of 2")
	}
}

// Behind a trusted /32 or /128 proxy that appends the peer it saw, the
// client cannot change its key by writing entries to the left of that
// address, so repeated attempts drain one bucket. A peer that is not a
// trusted proxy cannot use the header at all.
func TestTrustedProxyContract(t *testing.T) {
	for _, tt := range []struct {
		name, proxy, peer, client, want string
	}{
		{"IPv4 /32 proxy", "10.0.0.2/32", "10.0.0.2:1234", "203.0.113.9", "203.0.113.9/32"},
		{"IPv6 /128 proxy", "2001:db8:ffff::2/128", "[2001:db8:ffff::2]:1234", "2001:db8:1:2::9", "2001:db8:1:2::/64"},
		{"untrusted peer", "10.0.0.2/32", "198.51.100.7:1234", "203.0.113.9", "198.51.100.7/32"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			trusted, err := ParseTrustedProxies(tt.proxy)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
			limits := NewAuthLimits(trusted, func() time.Time { return now })
			allowed := 0
			for i := range 10 {
				r := httptest.NewRequest("POST", "/signin", nil)
				r.RemoteAddr = tt.peer
				// The client writes a different address on the left each
				// time; the proxy appends the peer it saw.
				r.Header.Set("X-Forwarded-For", fmt.Sprintf("192.0.2.%d, %s", i+1, tt.client))
				if got := ClientKey(r, trusted); got != netip.MustParsePrefix(tt.want) {
					t.Fatalf("attempt %d: key %s, want %s", i+1, got, tt.want)
				}
				if limits.Allow(limits.SignIn, r) {
					allowed++
				}
			}
			if allowed != signInBurst {
				t.Fatalf("%d of 10 attempts admitted, want one bucket's burst of %d", allowed, signInBurst)
			}
		})
	}
}
