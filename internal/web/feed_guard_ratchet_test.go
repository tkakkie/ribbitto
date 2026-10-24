package web

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func checkFeedValue(metric, part string, actual, budget float64, bytes bool) []string {
	var violations []string
	if actual > budget {
		violations = append(violations, fmt.Sprintf("%s %s: actual %.3f exceeds budget %.3f", metric, part, actual, budget))
	}
	maximum := actual
	if bytes {
		// #1536: only bytes retain headroom for class names, text and translations.
		maximum = math.Ceil(actual * 1.05)
	}
	if budget > maximum {
		violations = append(violations, fmt.Sprintf("%s %s: budget %.3f exceeds allowed %.3f", metric, part, budget, maximum))
	}
	return violations
}

func TestFeedGuardBudgetMutations(t *testing.T) {
	samples := feedSamples(t, false)
	latest := feedFirstRender(t).values
	for i, limit := range feedLimits()[:5] {
		slope := float64(samples[2][i]-samples[0][i]) / 150
		for _, tt := range []struct {
			part           string
			actual, budget float64
		}{
			{"fixed", float64(samples[0][i]) - 50*slope, limit.fixed},
			{"per message", slope, limit.slope},
			{"first render", float64(latest[i]), float64(limit.latest)},
		} {
			t.Run(limit.metric+"/"+tt.part, func(t *testing.T) {
				if got := checkFeedValue(limit.metric, tt.part, tt.actual, tt.budget, false); len(got) != 0 {
					t.Fatalf("unmodified limit: %v", got)
				}
				tt.budget++
				got := checkFeedValue(limit.metric, tt.part, tt.actual, tt.budget, false)
				if len(got) != 1 || !strings.Contains(got[0], "exceeds allowed") {
					t.Errorf("raised limit: %v, want only headroom violation", got)
				}
			})
		}
	}
}

func TestFeedGuardBudgetPredicates(t *testing.T) {
	for _, tt := range []struct {
		name           string
		actual, budget float64
		bytes          bool
		want           string
	}{
		{"count headroom only", 10, 11, false, "exceeds allowed"},
		{"lowered count only", 10, 9, false, "exceeds budget"},
		{"reduced count and limit", 9, 9, false, ""},
		{"byte headroom only", 101, 108, true, "exceeds allowed"},
		{"rounded byte cap", 101, 107, true, ""},
		{"bytes without headroom", 101, 101, true, ""},
		{"bytes below measured", 101, 100, true, "exceeds budget"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := checkFeedValue("metric", "part", tt.actual, tt.budget, tt.bytes)
			if tt.want == "" && len(got) != 0 || tt.want != "" && (len(got) != 1 || !strings.Contains(got[0], tt.want)) {
				t.Errorf("violations %v, want %q", got, tt.want)
			}
		})
	}
}
