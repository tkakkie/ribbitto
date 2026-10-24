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
	violations := append(checkFeedStructure(samples, feedLimits()), checkFeedFirstRender(latest, feedLimits())...)
	if len(violations) != 0 {
		t.Fatalf("unmodified limits: %v", violations)
	}
	for i, limit := range feedLimits()[:5] {
		for _, part := range []string{"fixed", "per message", "first render"} {
			t.Run(limit.metric+"/"+part, func(t *testing.T) {
				limits := feedLimits()
				switch part {
				case "fixed":
					limits[i].fixed++
				case "per message":
					limits[i].slope++
				case "first render":
					limits[i].latest++
				}
				got := append(checkFeedStructure(samples, limits), checkFeedFirstRender(latest, limits)...)
				if len(got) != 1 || !strings.Contains(got[0], limit.metric+" "+part+":") || !strings.Contains(got[0], "exceeds allowed") {
					t.Errorf("raised limit: %v, want only %s %s headroom violation", got, limit.metric, part)
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
