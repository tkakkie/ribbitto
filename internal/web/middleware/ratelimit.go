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

// maxBuckets bounds each table's memory, whatever the number of clients.
const maxBuckets = 10_000

// networkBits is the IPv6 prefix every IPv6 client is also charged to. One
// party often holds a whole /48 (a common end-site and tunnel-broker
// assignment), so /64 keys alone would give it 65,536 budgets. It is this
// application's chosen boundary, not a standard: unrelated clients whose
// smaller prefixes share a /48 also share its budget.
const networkBits = 48

// RateLimiter keeps one token bucket per client key and, for IPv6 clients,
// one per /48, in memory. That is enough while ribbitto runs as one
// process; several processes would need a shared store.
type RateLimiter struct {
	limit   Limit
	network Limit // Burst 0: no per-network buckets
	now     func() time.Time
	max     int

	mu       sync.Mutex
	buckets  map[netip.Prefix]*bucket // per client key
	networks map[netip.Prefix]*bucket // per IPv6 /48
}

type bucket struct {
	limiter *rate.Limiter
	// last is when a token was last taken, never moved by a refused
	// request: otherwise hammering an empty bucket would keep it from ever
	// being evicted.
	last time.Time
}

// NewRateLimiter returns a limiter with per-client buckets only; now is
// time.Now outside tests.
func NewRateLimiter(limit Limit, now func() time.Time) *RateLimiter {
	return NewNetworkRateLimiter(limit, Limit{}, now)
}

// NewNetworkRateLimiter returns a limiter that also charges every IPv6
// client's attempts to its /48, with the network limit.
func NewNetworkRateLimiter(limit, network Limit, now func() time.Time) *RateLimiter {
	return &RateLimiter{
		limit: limit, network: network, now: now, max: maxBuckets,
		buckets: map[netip.Prefix]*bucket{}, networks: map[netip.Prefix]*bucket{},
	}
}

// Allow takes a token from the client's bucket, and from its /48's bucket
// for an IPv6 client, and reports whether it could. It takes both or
// neither: a request refused by either bucket takes no token from the
// other and creates no bucket.
func (l *RateLimiter) Allow(client netip.Prefix) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.lookup(l.buckets, client, l.limit, now)
	if !ok || b.limiter.TokensAt(now) < 1 {
		return false
	}
	if l.network.Burst > 0 && client.Addr().Is6() {
		network, _ := client.Addr().Prefix(networkBits)
		n, ok := l.lookup(l.networks, network, l.network, now)
		if !ok || n.limiter.TokensAt(now) < 1 {
			return false
		}
		take(l.networks, network, n, now)
	}
	take(l.buckets, client, b, now)
	return true
}

// take spends one token that the caller has checked is there, and stores
// the bucket.
func take(table map[netip.Prefix]*bucket, key netip.Prefix, b *bucket, now time.Time) {
	b.limiter.AllowN(now, 1)
	b.last = now
	table[key] = b
}

// lookup returns the key's bucket, or a new full one that is stored only
// once a token is taken from it. It reports false when the table is full
// and nothing can be evicted. That fails closed: the client has had nothing
// processed, so the full bucket it gets once there is room grants no extra
// attempts. A shared overflow bucket would: moving from it to a fresh
// bucket would hand out a second burst.
func (l *RateLimiter) lookup(table map[netip.Prefix]*bucket, key netip.Prefix, limit Limit, now time.Time) (*bucket, bool) {
	if b, ok := table[key]; ok {
		return b, true
	}
	if len(table) >= l.max {
		evict(table, limit, now)
	}
	if len(table) >= l.max {
		return nil, false
	}
	return &bucket{limiter: rate.NewLimiter(rate.Every(limit.Every), limit.Burst)}, true
}

// evict drops buckets idle long enough to have refilled completely. Such a
// bucket is indistinguishable from a new one, so dropping it never grants
// an extra burst. Idle counts from the last token taken: a refused request
// takes none, so it neither refills nor drains the bucket.
func evict(table map[netip.Prefix]*bucket, limit Limit, now time.Time) {
	refill := time.Duration(limit.Burst) * limit.Every
	for key, b := range table {
		if now.Sub(b.last) >= refill {
			delete(table, key)
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
// X-Forwarded-For address that is not a trusted proxy. trusted lists proxy
// peers trusted to forward the client address (RIBBITTO_TRUSTED_PROXIES),
// never ordinary clients, and each such proxy must append the peer it saw
// or overwrite the header (docs/architecture.md, reverse-proxy contract).
// Under that contract, reading from the right means a client cannot pick
// its own key by adding entries on the left. A missing header, one listing only trusted proxies, or a malformed
// entry at or right of the first untrusted address (one the trusted proxies
// appended) falls back to the peer. Entries further left are written by the
// client and never parsed: if a malformed one forced the fallback, a client
// could choose to share the proxy's key. IPv6 clients are keyed by their
// /64, since one host usually controls a whole /64; RateLimiter also
// charges their /48.
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

// The limits on the authentication forms. They are small because each
// attempt costs an Argon2id hash and a failed one is a guess.
const (
	// Sign-in: enough for typos; at most 5 + 300 = 305 attempts in a
	// client's first hour.
	signInBurst = 5
	signInEvery = 12 * time.Second
	// A /48 gets twice a client's rate, shared by all its /64s: at most
	// 10 + 600 = 610 in the first hour.
	signInNetworkBurst = 10
	signInNetworkEvery = 6 * time.Second
	// Sign-up and setup, which a person does once: at most 3 + 6 = 9 in a
	// client's first hour, and 6 + 12 = 18 for a /48.
	onceBurst        = 3
	onceEvery        = 10 * time.Minute
	onceNetworkBurst = 6
	onceNetworkEvery = 5 * time.Minute
)

// AuthLimits are the limits on the authentication forms, per client key
// and per IPv6 /48.
//
// A client bucket stays unevictable for burst × interval after its last
// admitted request: 60 s for sign-in, 30 minutes for sign-up and setup. In
// that window one /48 is admitted at most 10 + 60/6 = 20 sign-in or
// 6 + 30/5 = 12 sign-up or setup requests. It can therefore hold at most
// that many unevictable client buckets. It may leave more evictable ones
// behind, but those are dropped as soon as the table is full. To pin a
// route's 10,000-entry client table, that is, keep it full of unevictable
// buckets, an IPv6-only attack needs at least 500 (sign-in) or 834 (sign-up,
// setup) distinct /48s, and pinning the /48 table needs 10,000 /48s. An
// IPv4-only attack needs 10,000 addresses to pin the client table, which
// IPv4 addresses and IPv6 /64s share, so a mix of both can pin it together.
// Accepted: whoever holds that many addresses can still turn new clients
// away until buckets become evictable, but cannot get past the limits or
// grow memory.
type AuthLimits struct {
	SignIn, SignUp, Setup *RateLimiter
	// Trusted proxies whose X-Forwarded-For is believed (ClientKey).
	Trusted []netip.Prefix
}

// NewAuthLimits returns the limits; now is time.Now outside tests.
func NewAuthLimits(trusted []netip.Prefix, now func() time.Time) *AuthLimits {
	signIn := Limit{Burst: signInBurst, Every: signInEvery}
	signInNetwork := Limit{Burst: signInNetworkBurst, Every: signInNetworkEvery}
	once := Limit{Burst: onceBurst, Every: onceEvery}
	onceNetwork := Limit{Burst: onceNetworkBurst, Every: onceNetworkEvery}
	return &AuthLimits{
		SignIn:  NewNetworkRateLimiter(signIn, signInNetwork, now),
		SignUp:  NewNetworkRateLimiter(once, onceNetwork, now),
		Setup:   NewNetworkRateLimiter(once, onceNetwork, now),
		Trusted: trusted,
	}
}

// Allow reports whether the request's client may make one more attempt
// against limiter.
func (a *AuthLimits) Allow(limiter *RateLimiter, r *http.Request) bool {
	return limiter.Allow(ClientKey(r, a.Trusted))
}
