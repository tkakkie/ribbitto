package web

// Temporary #1444 probe: deliberately red even when every scenario passes.
import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type diagProbeCell struct {
	iterations, failures, ignoredContexts int
	seen                                  map[string]bool
	samples                               []string
}

func (c *diagProbeCell) add(out diagOutcome) {
	c.iterations++
	c.ignoredContexts += out.ignoredContexts
	if out.failed {
		c.failures++
	}
	kinds := out.kinds
	if out.failed && len(kinds) == 0 {
		kinds = []string{"assertion/cleanup (no CDP error)"}
	}
	if c.seen == nil {
		c.seen = make(map[string]bool)
	}
	var first []string
	for _, kind := range kinds {
		if !c.seen[kind] && len(c.samples) < 3 {
			c.seen[kind] = true
			first = append(first, kind)
		}
	}
	if len(first) > 0 {
		c.samples = append(c.samples, "FIRST "+strings.Join(first, "; ")+"\n"+out.detail)
	}
}

func TestBrowserFlakeProbe(t *testing.T) {
	bin, ok := launcher.LookPath()
	if !ok {
		browserUnavailable(t, "no installed browser")
	}
	// This is a stress scheduling budget, not a longer scenario timeout. Kill
	// all remaining browsers at 260 s, including any stuck SSE/server cleanup.
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), diagProbeBudget{}, true), 260*time.Second)
	defer cancel()
	scheduleUntil := time.Now().Add(235 * time.Second)
	var mu sync.Mutex
	cells := make(map[string]*diagProbeCell)
	var order []string
	cell := func(name string) *diagProbeCell {
		if cells[name] == nil {
			cells[name] = &diagProbeCell{}
			order = append(order, name)
		}
		return cells[name]
	}
	observe := func(t *testing.T, c *diagProbeCell) func(diagOutcome) {
		out := diagOutcome{detail: t.Name() + ": launch/connect failed before observation"}
		// Register before launch so fresh-browser teardown failures count too.
		t.Cleanup(func() {
			if t.Failed() && !out.failed {
				out.detail += "\nFAILED outside scenario observation (launch/browser teardown)"
			}
			out.failed = t.Failed()
			mu.Lock()
			defer mu.Unlock()
			c.add(out)
		})
		return func(value diagOutcome) { out = value }
	}
	run := func(t *testing.T, b *rod.Browser, report func(diagOutcome), scenario func(*testing.T, *rod.Browser)) {
		b = diagScenarioResult(t, b, report)
		scenario(t, b)
	}
	targets := []struct {
		name string
		fn   func(*testing.T, *rod.Browser)
	}{
		{"arrival", testArrivalBrowser}, {"motion-touch", testMotionTouchBrowser}, {"stream-reset-alert-unconfirmed", diagResetAlert},
	}
	available := func(until time.Time) bool {
		return ctx.Err() == nil && time.Now().Before(until) && time.Now().Before(scheduleUntil)
	}
	for _, mode := range []string{"fresh", "reused"} {
		t.Run(mode, func(t *testing.T) {
			for _, target := range targets {
				cell(mode + "/" + target.name)
			}
			var reused *rod.Browser
			if mode == "reused" && available(scheduleUntil) {
				reused = launchScenarioBrowser(t, ctx, bin)
			}
			for _, target := range targets {
				c := cell(mode + "/" + target.name)
				t.Run(target.name, func(t *testing.T) {
					until := time.Now().Add(25 * time.Second)
					for i := 0; available(until); i++ {
						t.Run(fmt.Sprint(i), func(t *testing.T) {
							report := observe(t, c)
							b := reused
							if mode == "fresh" {
								b = launchScenarioBrowser(t, ctx, bin)
							}
							run(t, b, report, target.fn)
						})
					}
				})
			}
		})
	}
	t.Run("pool", func(t *testing.T) {
		neighbours := append(targets,
			struct {
				name string
				fn   func(*testing.T, *rod.Browser)
			}{"side-panel", testSidePanelBrowser},
			struct {
				name string
				fn   func(*testing.T, *rod.Browser)
			}{"sidebar-scroll", testSidebarScrollBrowser},
			struct {
				name string
				fn   func(*testing.T, *rod.Browser)
			}{"message-link", testMessageLinkBrowser})
		counts := make([]*diagProbeCell, len(neighbours))
		for i, n := range neighbours {
			counts[i] = cell("pool/" + n.name)
		}
		until := time.Now().Add(60 * time.Second)
		if !available(until) {
			return
		}
		pool := make(chan *rod.Browser, 3)
		for range 3 {
			pool <- launchScenarioBrowser(t, ctx, bin)
		}
		// Every wave has three independent browsers, even with go test -parallel=2.
		// The first two waves explicitly overlap duplicates of both flaky cases;
		// later waves retain the real sidebar/link/reset neighbours from TestBrowser.
		waves := [][3]int{{0, 0, 1}, {1, 1, 0}, {2, 3, 4}, {5, 0, 1}}
		for wave := 0; available(until); wave++ {
			var wg sync.WaitGroup
			for lane, index := range waves[wave%len(waves)] {
				wg.Go(func() {
					n := neighbours[index]
					t.Run(fmt.Sprintf("%d/%d/%s", wave, lane, n.name), func(t *testing.T) {
						b := <-pool
						t.Cleanup(func() { pool <- b })
						run(t, b, observe(t, counts[index]), n.fn)
					})
				})
			}
			wg.Wait()
		}
	})
	var results []string
	for _, name := range order {
		c := cells[name]
		results = append(results, fmt.Sprintf("%s iterations=%d failures=%d ignored ErrCtxNotFound=%d\n%s", name, c.iterations, c.failures, c.ignoredContexts, strings.Join(c.samples, "\n\n")))
	}
	t.Errorf("PROBE RESULT\nfresh browser per iteration / one reused browser / pool of 3 with real neighbours\n25 s per fresh/reused target cell; 60 s shared pool; scheduling cap 235 s; kill watchdog 260 s; budgetEnded=%t\n%s", ctx.Err() != nil, strings.Join(results, "\n\n"))
}

// The alert-unconfirmed branch of testStreamResetBrowser, using its real
// fixture, clock and assertions without running its other reset predicates.
func diagResetAlert(t *testing.T, b *rod.Browser) {
	page, events, _, _ := pendingBrowser(t, b, false)
	resetClock(page)
	page.MustEval(`() => {const a=document.getElementById('message-send-alert'); a.textContent=a.dataset.unconfirmed;}`)
	events <- realtime.Outgoing{Name: "reset"}
	page.MustWait(`() => window.source.readyState===EventSource.CLOSED`)
	resetHeld(t, page)
	page.MustEval(`() => document.getElementById('message-send-alert').firstChild?.replaceData(0,100,'')`)
	resetReload(t, page)
}
