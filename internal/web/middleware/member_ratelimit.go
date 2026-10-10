package middleware

import (
	"sync"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"golang.org/x/time/rate"
)

const (
	memberBurst      = 4
	memberMaxBuckets = 10_000
)

type memberLimitKey struct {
	organizationID kernel.ID
	memberID       kernel.ID
}

// MemberRateLimiter bounds typing signals per organisation/member pair in
// one process. Share one instance across all signal callers in that process.
type MemberRateLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[memberLimitKey]*rate.Limiter
}

// NewMemberRateLimiter returns a limiter with burst 4 and one token per
// second; now is time.Now outside tests.
func NewMemberRateLimiter(now func() time.Time) *MemberRateLimiter {
	return &MemberRateLimiter{
		now: now, buckets: make(map[memberLimitKey]*rate.Limiter),
	}
}

// Allow takes one token for trusted, server-resolved organisation/member
// IDs. The authenticated web adapter supplies them, never a request body.
// Channels, topics, sessions and addresses share the member's budget.
// Refusal creates no bucket and does not extend a bucket's retention.
func (l *MemberRateLimiter) Allow(organizationID, memberID kernel.ID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	key := memberLimitKey{organizationID: organizationID, memberID: memberID}
	b, exists := l.buckets[key]
	if !exists {
		if len(l.buckets) >= memberMaxBuckets {
			for key, b := range l.buckets {
				// Replacing a partially full bucket would grant extra tokens.
				// TokensAt does not change it, so refusals cannot pin it.
				if b.TokensAt(now) >= memberBurst {
					delete(l.buckets, key)
				}
			}
		}
		if len(l.buckets) >= memberMaxBuckets {
			return false
		}
		b = rate.NewLimiter(rate.Every(time.Second), memberBurst)
	}
	// AllowN rounds a sub-nanosecond wait to zero; require a whole token
	// first so a request just before the refill boundary stays refused.
	if b.TokensAt(now) < 1 || !b.AllowN(now, 1) {
		return false
	}
	l.buckets[key] = b
	return true
}
