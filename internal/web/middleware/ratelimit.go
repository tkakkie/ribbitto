package middleware

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limit is a token bucket: Burst requests at once, then one every Every.
type Limit struct {
	Burst int
	Every time.Duration
}

// maxBuckets bounds each limiter's memory, whatever the number of clients.
const maxBuckets = 10_000

// RateLimiter keeps one token bucket per client key, in memory. That is
// enough while ribbitto runs as one process; several processes would need a
// shared store.
type RateLimiter struct {
	limit Limit
	now   func() time.Time
	max   int

	mu      sync.Mutex
	buckets map[netip.Prefix]*bucket
}

type bucket struct {
	limiter *rate.Limiter
	last    time.Time
}

// NewRateLimiter returns a limiter; now is time.Now outside tests.
func NewRateLimiter(limit Limit, now func() time.Time) *RateLimiter {
	return &RateLimiter{limit: limit, now: now, max: maxBuckets, buckets: map[netip.Prefix]*bucket{}}
}

// Allow takes a token from the client's bucket and reports whether there
// was one.
func (l *RateLimiter) Allow(client netip.Prefix) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[client]
	if !ok {
		if len(l.buckets) >= l.max {
			l.evict(now)
		}
		if len(l.buckets) >= l.max {
			// Fail closed. The client has had nothing processed, so the full
			// bucket it gets once there is room grants no extra attempts. A
			// shared overflow bucket would: moving from it to a fresh bucket
			// would hand out a second burst.
			return false
		}
		b = &bucket{limiter: rate.NewLimiter(rate.Every(l.limit.Every), l.limit.Burst)}
		l.buckets[client] = b
	}
	b.last = now
	return b.limiter.AllowN(now, 1)
}

// evict drops buckets idle long enough to have refilled completely. Such a
// bucket is indistinguishable from a new one, so dropping it never grants
// an extra burst.
func (l *RateLimiter) evict(now time.Time) {
	refill := time.Duration(l.limit.Burst) * l.limit.Every
	for client, b := range l.buckets {
		if now.Sub(b.last) >= refill {
			delete(l.buckets, client)
		}
	}
}

// ParseTrustedProxies parses RIBBITTO_TRUSTED_PROXIES: comma-separated CIDRs.
func ParseTrustedProxies(value string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, field := range strings.Split(value, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(field)
		if err != nil {
			return nil, fmt.Errorf("parsing trusted proxy %q: %w", field, err)
		}
		// Addresses are compared unmapped (ClientKey), so an IPv4-mapped
		// CIDR must become its IPv4 equivalent or it would never match.
		if prefix.Addr().Is4In6() {
			if prefix.Bits() < 96 {
				return nil, fmt.Errorf("trusted proxy %q: an IPv4-mapped CIDR needs a prefix length of at least 96", field)
			}
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

// ClientKey identifies the client for rate limiting. It is the peer
// address, unless the peer is a trusted proxy: then it is the rightmost
// X-Forwarded-For address that is not a trusted proxy. Reading from the
// right means a client cannot pick its own key by adding entries on the
// left. A missing or malformed header, or one listing only trusted proxies,
// falls back to the peer. IPv6 clients are keyed by their /64, since one
// host usually controls a whole /64.
func ClientKey(r *http.Request, trusted []netip.Prefix) netip.Prefix {
	peer := peerAddr(r)
	if !isTrusted(peer, trusted) {
		return keyFor(peer)
	}
	var entries []string
	for _, value := range r.Header.Values("X-Forwarded-For") {
		entries = append(entries, strings.Split(value, ",")...)
	}
	for i := len(entries) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(entries[i]))
		if err != nil || addr.Zone() != "" {
			return keyFor(peer)
		}
		addr = addr.Unmap()
		if !isTrusted(addr, trusted) {
			return keyFor(addr)
		}
	}
	return keyFor(peer)
}

func peerAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		// net/http always sets a parseable RemoteAddr; an unparseable one
		// shares a single key rather than escaping the limit.
		return netip.IPv6Unspecified()
	}
	return addr.WithZone("").Unmap()
}

func isTrusted(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, prefix := range trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func keyFor(addr netip.Addr) netip.Prefix {
	bits := 32
	if addr.Is6() {
		bits = 64
	}
	prefix, _ := addr.Prefix(bits)
	return prefix
}

// AuthLimits are the per-client limits on the authentication forms. They
// are small because each attempt costs an Argon2id hash and a failed one is
// a guess: sign-in allows a burst of 5 then one every 12 s (enough for
// typos, about 300 guesses an hour per client); sign-up and setup, which a
// person does once, a burst of 3 then one every 10 minutes.
type AuthLimits struct {
	SignIn, SignUp, Setup *RateLimiter
	// Trusted proxies whose X-Forwarded-For is believed (ClientKey).
	Trusted []netip.Prefix
}

// NewAuthLimits returns the limits; now is time.Now outside tests.
func NewAuthLimits(trusted []netip.Prefix, now func() time.Time) *AuthLimits {
	return &AuthLimits{
		SignIn:  NewRateLimiter(Limit{Burst: 5, Every: 12 * time.Second}, now),
		SignUp:  NewRateLimiter(Limit{Burst: 3, Every: 10 * time.Minute}, now),
		Setup:   NewRateLimiter(Limit{Burst: 3, Every: 10 * time.Minute}, now),
		Trusted: trusted,
	}
}

// Allow reports whether the request's client may make one more attempt
// against limiter.
func (a *AuthLimits) Allow(limiter *RateLimiter, r *http.Request) bool {
	return limiter.Allow(ClientKey(r, a.Trusted))
}
