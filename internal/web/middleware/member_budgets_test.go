package middleware

import (
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestMemberBudgets(t *testing.T) {
	for _, tt := range []struct {
		name       string
		newLimiter func(func() time.Time) *MemberRateLimiter
		burst      int
		interval   time.Duration
		concurrent int
	}{
		// docs/architecture/rate-limits.md, "Member admission for branching":
		// burst 20, one token every 6 s, unlimited concurrency.
		{"branch", NewBranchRateLimiter, 20, 6 * time.Second, 0},
		// docs/architecture/member-admission.md, "Posting and pin-change admission" table ("Channel and topic posts"):
		// burst 20, 1/s, 4 concurrent requests.
		{"post", NewPostRateLimiter, 20, time.Second, 4},
		// docs/architecture/member-admission.md, "Posting and pin-change admission" table ("Pin and unpin together"):
		// burst 10, one per 2 s, unlimited concurrency.
		{"pin", NewPinRateLimiter, 10, 2 * time.Second, 0},
		// docs/architecture/rate-limits.md, "Member admission for sidebar resyncs":
		// 32 requests/s, burst 32, unlimited concurrency.
		{"sidebar resync", NewSidebarResyncRateLimiter, 32, time.Second / 32, 0},
		// docs/architecture/rate-limits.md, "Member admission for mention candidates":
		// 4/s, burst 8, 2 concurrent requests.
		{"mention", NewMentionRateLimiter, 8, time.Second / 4, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
			l := tt.newLimiter(func() time.Time { return now })
			organization, member := kernel.ID{1}, kernel.ID{2}
			for i := range tt.burst {
				if !l.Allow(organization, member) {
					t.Errorf("request %d within burst of %d refused", i+1, tt.burst)
					break
				}
			}
			if l.Allow(organization, member) {
				t.Errorf("admitted beyond burst of %d", tt.burst)
			}
			if !l.Allow(organization, kernel.ID{3}) || !l.Allow(kernel.ID{4}, member) {
				t.Error("budgets shared across members or organisations")
			}
			now = now.Add(tt.interval - time.Nanosecond)
			if l.Allow(organization, member) {
				t.Error("refilled early")
			} else {
				now = now.Add(time.Nanosecond)
				if !l.Allow(organization, member) || l.Allow(organization, member) {
					t.Error("want exactly one refilled token")
				}
			}
			cl := tt.newLimiter(func() time.Time { return now })
			if tt.concurrent > 0 {
				var releases []func()
				for i := range tt.concurrent {
					release, ok := cl.Admit(organization, member)
					if !ok {
						t.Errorf("slot %d within concurrent ceiling of %d refused", i+1, tt.concurrent)
						break
					}
					releases = append(releases, release)
				}
				if len(releases) == tt.concurrent {
					if release, ok := cl.Admit(organization, member); ok {
						release()
						t.Errorf("admitted beyond concurrent ceiling of %d", tt.concurrent)
					}
					releases[0]()
					release, ok := cl.Admit(organization, member)
					if !ok {
						t.Error("released slot not admitted")
					} else {
						release()
					}
					for _, release := range releases[1:] {
						release()
					}
				}
			} else {
				// Admit consumes a burst token per call, so tt.burst is the most a fresh
				// limiter can admit at once; holding all of them exposes any hidden
				// concurrent ceiling below the burst.
				var releases []func()
				for i := range tt.burst {
					release, ok := cl.Admit(organization, member)
					if !ok {
						t.Errorf("slot %d within burst of %d refused", i+1, tt.burst)
						break
					}
					releases = append(releases, release)
				}
				for _, release := range releases {
					release()
				}
			}
		})
	}
}
