package web

import (
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

func TestMemberLimitsZeroOption(t *testing.T) {
	now := func() time.Time { return time.Unix(1, 0) }
	for _, tt := range []struct {
		name    string
		options []MemberLimitsOption
	}{
		{"no options", nil},
		{"zero option", []MemberLimitsOption{{}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l := NewMemberLimits(now, tt.options...)
			limits := []*middleware.MemberRateLimiter{l.SendKey(), l.Mention(), l.Signal(), l.Read(), l.Create(), l.Resync(), l.Branch(), l.Post(), l.Pin()}
			bursts := []int{4, 8, 4, 16, 10, 32, 20, 20, 10}
			seen := make(map[*middleware.MemberRateLimiter]bool)
			for i, limit := range limits {
				if limit == nil || seen[limit] {
					t.Fatalf("limiter %d is missing or shared", i)
				}
				seen[limit] = true
				for range bursts[i] {
					if !limit.Allow(kernel.ID{}, kernel.ID{}) {
						t.Fatalf("limiter %d refused before its burst", i)
					}
				}
				if limit.Allow(kernel.ID{}, kernel.ID{}) {
					t.Fatalf("limiter %d exceeded its burst", i)
				}
			}
		})
	}
}

func TestWithReadAdmissionBudget(t *testing.T) {
	t.Run("burst and refill", func(t *testing.T) {
		now := time.Unix(1, 0)
		clock := func() time.Time { return now }
		l := NewMemberLimits(clock, WithReadAdmission(clock, 5)).Read()
		for _, tt := range []struct {
			advance time.Duration
			allowed int
		}{
			{0, 16},
			{250*time.Millisecond - time.Nanosecond, 0},
			{time.Nanosecond, 1},
		} {
			now = now.Add(tt.advance)
			for range tt.allowed {
				if !l.Allow(kernel.ID{}, kernel.ID{}) {
					t.Fatalf("refused within budget of %d", tt.allowed)
				}
			}
			if l.Allow(kernel.ID{}, kernel.ID{}) {
				t.Fatalf("admitted beyond budget of %d", tt.allowed)
			}
		}
	})
	t.Run("concurrency", func(t *testing.T) {
		now := func() time.Time { return time.Unix(1, 0) }
		l := NewMemberLimits(now, WithReadAdmission(now, 5)).Read()
		for range 5 {
			release, ok := l.Admit(kernel.ID{}, kernel.ID{})
			if !ok {
				t.Fatal("refused within concurrency ceiling")
			}
			defer release()
		}
		if release, ok := l.Admit(kernel.ID{}, kernel.ID{}); ok {
			release()
			t.Fatal("admitted beyond concurrency ceiling")
		}
	})
}

func TestMemberLimitsIndependent(t *testing.T) {
	now := func() time.Time { return time.Unix(1, 0) }
	for _, options := range [][]MemberLimitsOption{nil, {WithReadAdmission(now, 5)}} {
		for exhausted := range 9 {
			l := NewMemberLimits(now, options...)
			limits := []*middleware.MemberRateLimiter{l.SendKey(), l.Mention(), l.Signal(), l.Read(), l.Create(), l.Resync(), l.Branch(), l.Post(), l.Pin()}
			bursts := []int{4, 8, 4, 16, 10, 32, 20, 20, 10}
			for i, limit := range limits {
				if limit == nil {
					t.Fatalf("limiter %d is missing", i)
				}
				for j := range i {
					if limit == limits[j] {
						t.Fatalf("limiters %d and %d are shared", j, i)
					}
				}
			}
			for range bursts[exhausted] {
				if !limits[exhausted].Allow(kernel.ID{}, kernel.ID{}) {
					t.Fatalf("limiter %d refused before its burst", exhausted)
				}
			}
			if limits[exhausted].Allow(kernel.ID{}, kernel.ID{}) {
				t.Fatalf("limiter %d exceeded its burst", exhausted)
			}
			for i, limit := range limits {
				if i == exhausted {
					continue
				}
				for range bursts[i] {
					if !limit.Allow(kernel.ID{}, kernel.ID{}) {
						t.Fatalf("exhausting limiter %d consumed limiter %d's budget", exhausted, i)
					}
				}
			}
		}
	}
}
