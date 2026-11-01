package web

import (
	"time"

	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

// MemberLimits keeps each request kind's budget independent. Its zero value
// is invalid; construct it once and share it across the process's handlers.
type MemberLimits struct {
	sendKey, mention, signal, read, create, image, resync, branch, post, pin *middleware.MemberRateLimiter
}

// MemberLimitsOption adjusts construction without accepting shared limiters.
// Its zero value is a no-op.
type MemberLimitsOption struct {
	apply func(*MemberLimits)
}

// WithReadAdmission overrides the read clock and slot ceiling for trusted fixtures.
func WithReadAdmission(now func() time.Time, concurrent int) MemberLimitsOption {
	return MemberLimitsOption{func(l *MemberLimits) {
		l.read = middleware.NewReadRateLimiterWithConcurrency(now, concurrent)
	}}
}

// NewMemberLimits creates all ten production budgets; now is time.Now outside tests.
func NewMemberLimits(now func() time.Time, options ...MemberLimitsOption) MemberLimits {
	l := MemberLimits{
		sendKey: middleware.NewMemberRateLimiter(now), mention: middleware.NewMentionRateLimiter(now),
		signal: middleware.NewMemberRateLimiter(now), read: middleware.NewReadRateLimiter(now),
		create: middleware.NewChannelCreationRateLimiter(now), image: middleware.NewChannelImageRateLimiter(now), resync: middleware.NewSidebarResyncRateLimiter(now),
		branch: middleware.NewBranchRateLimiter(now), post: middleware.NewPostRateLimiter(now), pin: middleware.NewPinRateLimiter(now),
	}
	for _, option := range options {
		if option.apply != nil {
			option.apply(&l)
		}
	}
	return l
}

// SendKey returns the send-key preference budget.
func (l MemberLimits) SendKey() *middleware.MemberRateLimiter { return l.sendKey }

// Mention returns the mention-candidate budget.
func (l MemberLimits) Mention() *middleware.MemberRateLimiter { return l.mention }

// Signal returns the typing-signal budget.
func (l MemberLimits) Signal() *middleware.MemberRateLimiter { return l.signal }

// Read returns the read budget.
func (l MemberLimits) Read() *middleware.MemberRateLimiter { return l.read }

// Create returns the channel-creation budget.
func (l MemberLimits) Create() *middleware.MemberRateLimiter { return l.create }

// Image returns the channel image change budget.
func (l MemberLimits) Image() *middleware.MemberRateLimiter { return l.image }

// Resync returns the sidebar-resync budget.
func (l MemberLimits) Resync() *middleware.MemberRateLimiter { return l.resync }

// Branch returns the branching budget.
func (l MemberLimits) Branch() *middleware.MemberRateLimiter { return l.branch }

// Post returns the posting budget.
func (l MemberLimits) Post() *middleware.MemberRateLimiter { return l.post }

// Pin returns the pin-change budget.
func (l MemberLimits) Pin() *middleware.MemberRateLimiter { return l.pin }
