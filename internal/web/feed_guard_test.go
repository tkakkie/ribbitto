package web

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type feedMeasurement struct {
	doc    *html.Node
	values [6]int
}
type feedLimit struct {
	metric       string
	fixed, slope float64
	latest       int
}

func measureFeed(t *testing.T, markup string) feedMeasurement {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		t.Fatal(err)
	}
	if countElements(doc, func(n *html.Node) bool { return feedHas(n, "hx-disable", "data-hx-disable") }) != 0 {
		t.Fatal("htmx mirror requires no hx-disable or data-hx-disable")
	}
	body := find(doc, atom.Body)
	if body == nil {
		t.Fatal("missing body root for htmx processing")
	}
	return feedMeasurement{doc, [6]int{countElements(doc, nil), countElements(doc, func(n *html.Node) bool { return n.Data == "form" }), countElements(doc, feedSubmit), countElements(doc, hasHTMXAttribute), 1 + countElements(body, feedHTMXSelected), len(markup)}}
}

func feedLimits() []feedLimit {
	return []feedLimit{
		{"elements", feedBudgetElementsFixed, feedBudgetElementsSlope, feedBudgetElementsLatest},
		{"forms", feedBudgetFormsFixed, feedBudgetFormsSlope, feedBudgetFormsLatest},
		{"[type=submit]", feedBudgetSubmitFixed, feedBudgetSubmitSlope, feedBudgetSubmitLatest},
		{"hx-* carriers", feedBudgetHXFixed, feedBudgetHXSlope, feedBudgetHXLatest},
		{"htmx-selected", feedBudgetHTMXFixed, feedBudgetHTMXSlope, feedBudgetHTMXLatest},
		{"HTML bytes", feedBudgetBytesFixed, feedBudgetBytesSlope, feedBudgetBytesLatest},
	}
}

type feedGuardPage struct {
	name          string
	fixture       func(*testing.T, int) view.ChannelPage
	limits        []feedLimit
	wholeElements int
}

func feedGuardPages() []feedGuardPage {
	resolved := feedLimits()
	resolved[0] = feedLimit{"elements", feedBudgetResolvedElementsFixed, feedBudgetElementsSlope, feedBudgetResolvedElementsLatest}
	resolved[1] = feedLimit{"forms", feedBudgetResolvedFormsFixed, feedBudgetFormsSlope, feedBudgetResolvedFormsLatest}
	resolved[2] = feedLimit{"[type=submit]", feedBudgetResolvedSubmitFixed, feedBudgetSubmitSlope, feedBudgetResolvedSubmitLatest}
	resolved[4] = feedLimit{"htmx-selected", feedBudgetResolvedHTMXFixed, feedBudgetHTMXSlope, feedBudgetResolvedHTMXLatest}
	resolved[5] = feedLimit{"HTML bytes", feedBudgetResolvedBytesFixed, feedBudgetResolvedBytesSlope, feedBudgetBytesLatest}
	return []feedGuardPage{
		{"channel", feedFixture, feedLimits(), 1294},
		// https://github.com/tkakkie/ribbitto/issues/2183#issuecomment-6687852298 approves the resolved-topic whole-page cap.
		{"resolved topic", resolvedTopicFixture, resolved, 1312},
	}
}

// The endpoints define the line; the middle sample independently detects
// nonlinear growth. Counts are exact; byte residuals allow digit/name variation.
func checkFeedStructure(samples [3][6]int, limits []feedLimit) []string {
	var violations []string
	for i, limit := range limits {
		slope := float64(samples[2][i]-samples[0][i]) / 150
		fixed := float64(samples[0][i]) - 50*slope
		residual := math.Abs(float64(3*samples[1][i]-2*samples[0][i]-samples[2][i])) / 3
		tolerance := float64(0)
		if i == 5 {
			tolerance = feedBudgetBytesLinearity
		}
		for _, check := range []struct {
			name           string
			actual, budget float64
		}{
			{"fixed", fixed, limit.fixed}, {"per message", slope, limit.slope}, {"linearity", residual, tolerance},
		} {
			if check.name != "linearity" {
				violations = append(violations, checkFeedValue(limit.metric, check.name, check.actual, check.budget, i == 5)...)
			} else if check.actual > check.budget {
				violations = append(violations, fmt.Sprintf("%s %s: actual %.3f exceeds budget %.3f", limit.metric, check.name, check.actual, check.budget))
			}
		}
	}
	return violations
}

func feedSamples(t *testing.T, page feedGuardPage, addButton bool) [3][6]int {
	t.Helper()
	var samples [3][6]int
	for i, size := range []int{50, 100, 200} {
		measured := measureFeed(t, renderIn(t, "en", view.ChannelScreen("/static/css/app.css", page.fixture(t, size))))
		if addButton {
			for node := range measured.doc.Descendants() {
				if feedMessageItem(node) {
					node.AppendChild(&html.Node{Type: html.ElementNode, Data: "button", DataAtom: atom.Button, Attr: []html.Attribute{{Key: "type", Val: "submit"}}})
				}
			}
			var markup bytes.Buffer
			if err := html.Render(&markup, measured.doc); err != nil {
				t.Fatal(err)
			}
			measured = measureFeed(t, markup.String())
		}
		samples[i] = measured.values
		for metric, limit := range page.limits {
			t.Logf("messages=%d %s=%d", size, limit.metric, measured.values[metric])
		}
	}
	return samples
}

func feedMessageItem(n *html.Node) bool {
	return n.Type == html.ElementNode && n.Data == "li" && feedHas(n, "data-event-seq") && n.Parent != nil && attr(n.Parent, "id") == "message-items"
}

func TestFeedGuardStructure(t *testing.T) {
	for _, page := range feedGuardPages() {
		t.Run(page.name, func(t *testing.T) {
			samples := feedSamples(t, page, false)
			for i, limit := range page.limits {
				slope := float64(samples[2][i]-samples[0][i]) / 150
				t.Logf("%s: fixed=%.3f per message=%.3f", limit.metric, float64(samples[0][i])-50*slope, slope)
			}
			for _, violation := range checkFeedStructure(samples, page.limits) {
				t.Error(violation)
			}
		})
	}
}

func TestFeedGuardCatchesButtonPerMessage(t *testing.T) {
	for _, page := range feedGuardPages() {
		t.Run(page.name, func(t *testing.T) {
			violations := strings.Join(checkFeedStructure(feedSamples(t, page, true), page.limits), "\n")
			t.Log(violations)
			for _, metric := range []string{"elements per message", "[type=submit] per message", "htmx-selected per message"} {
				if !strings.Contains(violations, metric) {
					t.Errorf("missing %s violation: %s", metric, violations)
				}
			}
		})
	}
}

func TestFeedGuardChecker(t *testing.T) {
	var base [3][6]int
	for j, n := range []int{50, 100, 200} {
		for i, l := range feedLimits() {
			base[j][i] = int(l.fixed + float64(n)*l.slope)
		}
	}
	for _, kind := range []string{"fixed", "per message", "linearity"} {
		sample := base
		for j, n := range []int{50, 100, 200} {
			switch kind {
			case "fixed":
				sample[j][0]++
			case "per message":
				sample[j][0] += n
			case "linearity":
				if j == 1 {
					sample[j][0]++
				}
			}
		}
		if got := strings.Join(checkFeedStructure(sample, feedLimits()), "\n"); !strings.Contains(got, "elements "+kind) {
			t.Errorf("%s regression missed: %s", kind, got)
		}
	}
}
