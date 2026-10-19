package web

import (
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
)

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
