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

type memberBucket struct {
	*rate.Limiter
	active int
}

// MemberRateLimiter bounds requests per organisation/member pair in
// one process. Share one instance per request type across its callers.
type MemberRateLimiter struct {
	mu                sync.Mutex
	now               func() time.Time
	rate              rate.Limit
	burst, concurrent int
	buckets           map[memberLimitKey]*memberBucket
}

// NewMemberRateLimiter returns a limiter with burst 4 and one token per
// second; now is time.Now outside tests.
func NewMemberRateLimiter(now func() time.Time) *MemberRateLimiter {
	return NewMemberRateLimiterWithBudget(now, 1, memberBurst, 0)
}

// NewReadRateLimiter returns initial, unmeasured read limits: 4/s, burst 16,
// and four concurrent requests per member. State is in memory per process.
func NewReadRateLimiter(now func() time.Time) *MemberRateLimiter {
	return NewReadRateLimiterWithConcurrency(now, 4)
}

// NewReadRateLimiterWithConcurrency keeps the read budget with a custom slot ceiling.
func NewReadRateLimiterWithConcurrency(now func() time.Time, concurrent int) *MemberRateLimiter {
	return NewMemberRateLimiterWithBudget(now, 4, 16, concurrent)
}

// NewChannelCreationRateLimiter returns an independent burst of 10, then one
// token per minute per organisation/member; share it across all create callers.
func NewChannelCreationRateLimiter(now func() time.Time) *MemberRateLimiter {
	return NewMemberRateLimiterWithBudget(now, 1.0/60, 10, 0)
}

// NewBranchRateLimiter returns an independent burst of 20, then one token
// every 6 s: tidying a channel move by move fits, while sustained moves can
// dirty each viewer's sidebar at most about ten times a minute.
func NewBranchRateLimiter(now func() time.Time) *MemberRateLimiter {
	return NewMemberRateLimiterWithBudget(now, 1.0/6, 20, 0)
}

// NewPostRateLimiter returns provisional, unmeasured limits: burst 20,
// then one token per second and four concurrent requests per member.
func NewPostRateLimiter(now func() time.Time) *MemberRateLimiter {
	return NewMemberRateLimiterWithBudget(now, 1, 20, 4)
}

// NewPinRateLimiter returns provisional, unmeasured limits: burst 10,
// then one token every two seconds per member.
func NewPinRateLimiter(now func() time.Time) *MemberRateLimiter {
	return NewMemberRateLimiterWithBudget(now, 0.5, 10, 0)
}

// NewSidebarResyncRateLimiter returns an independent budget of 32/s, burst 32.
// It covers 16 tabs retrying at the script's 8 s backoff cap with headroom.
func NewSidebarResyncRateLimiter(now func() time.Time) *MemberRateLimiter {
	return NewMemberRateLimiterWithBudget(now, 32, 32, 0)
}

// NewMentionRateLimiter returns initial, unmeasured candidate-search limits:
// 4/s, burst 8, and two concurrent requests per member to bound lookup work
// while allowing short bursts as the member types.
func NewMentionRateLimiter(now func() time.Time) *MemberRateLimiter {
	return NewMemberRateLimiterWithBudget(now, 4, 8, 2)
}

// NewMemberRateLimiterWithBudget supplies an explicit trusted budget, including
// for fixtures whose intentional request sequences exceed production limits.
// Rate and burst must be positive; concurrent zero leaves slots unlimited.
func NewMemberRateLimiterWithBudget(now func() time.Time, requestsPerSecond float64, burst, concurrent int) *MemberRateLimiter {
	return &MemberRateLimiter{now: now, rate: rate.Limit(requestsPerSecond), burst: burst, concurrent: concurrent,
		buckets: make(map[memberLimitKey]*memberBucket)}
}

// Admit takes a token and a concurrency slot without waiting. On success the
// caller must defer the returned release immediately, including panic paths.
func (l *MemberRateLimiter) Admit(organizationID, memberID kernel.ID) (func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.allow(organizationID, memberID)
	if !ok {
		return nil, false
	}
	b.active++
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		b.active--
	}, true
}

// Allow takes one token for trusted, server-resolved organisation/member
// IDs. The authenticated web adapter supplies them, never a request body.
// Channels, topics, sessions and addresses share the member's budget.
// Refusal creates no bucket and does not extend a bucket's retention.
func (l *MemberRateLimiter) Allow(organizationID, memberID kernel.ID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	_, ok := l.allow(organizationID, memberID)
	return ok
}

func (l *MemberRateLimiter) allow(organizationID, memberID kernel.ID) (*memberBucket, bool) {
	now := l.now()
	key := memberLimitKey{organizationID: organizationID, memberID: memberID}
	b, exists := l.buckets[key]
	if !exists {
		if len(l.buckets) >= memberMaxBuckets {
			for key, b := range l.buckets {
				// Replacing a partially full bucket would grant extra tokens.
				// TokensAt does not change it, so refusals cannot pin it.
				if b.active == 0 && b.TokensAt(now) >= float64(l.burst) {
					delete(l.buckets, key)
				}
			}
		}
		if len(l.buckets) >= memberMaxBuckets {
			return nil, false
		}
		b = &memberBucket{Limiter: rate.NewLimiter(l.rate, l.burst)}
	}
	// AllowN rounds a sub-nanosecond wait to zero; require a whole token
	// first so a request just before the refill boundary stays refused.
	if (l.concurrent > 0 && b.active >= l.concurrent) || b.TokensAt(now) < 1 || !b.AllowN(now, 1) {
		return nil, false
	}
	l.buckets[key] = b
	return b, true
}
