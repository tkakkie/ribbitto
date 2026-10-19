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

func TestBrowserFlakeProbe(t *testing.T) {
	bin, ok := launcher.LookPath()
	if !ok {
		browserUnavailable(t, "no installed browser")
	}
	// The probe watchdog kills its browsers at 110 s, including stuck cleanup.
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), diagProbeBudget{}, true), 110*time.Second)
	defer cancel()
	var mu sync.Mutex
	var results []string
	report := func(s string) { mu.Lock(); defer mu.Unlock(); results = append(results, s) }
	run := func(t *testing.T, b *rod.Browser, scenario func(*testing.T, *rod.Browser)) {
		b = diagScenario(t, b, report)
		scenario(t, b)
	}
	targets := []struct {
		name string
		fn   func(*testing.T, *rod.Browser)
	}{
		{"arrival", testArrivalBrowser}, {"motion-touch", testMotionTouchBrowser}, {"stream-reset-alert-unconfirmed", diagResetAlert},
	}
	const n = 2
	for _, mode := range []string{"fresh", "reused"} {
		t.Run(mode, func(t *testing.T) {
			for _, target := range targets {
				t.Run(target.name, func(t *testing.T) {
					var b *rod.Browser
					if mode == "reused" && ctx.Err() == nil {
						b = launchScenarioBrowser(t, ctx, bin)
					}
					for i := range n {
						t.Run(fmt.Sprint(i), func(t *testing.T) {
							if ctx.Err() != nil {
								t.Error("probe scheduling budget exhausted")
								return
							}
							if mode == "fresh" {
								b = launchScenarioBrowser(t, ctx, bin)
							}
							run(t, b, target.fn)
						})
					}
				})
			}
		})
	}
	t.Run("pool", func(t *testing.T) {
		if ctx.Err() != nil {
			t.Error("probe scheduling budget exhausted")
			return
		}
		pool := make(chan *rod.Browser, 3)
		for range 3 {
			pool <- launchScenarioBrowser(t, ctx, bin)
		}
		neighbours := append(targets, struct {
			name string
			fn   func(*testing.T, *rod.Browser)
		}{"side-panel", testSidePanelBrowser}, struct {
			name string
			fn   func(*testing.T, *rod.Browser)
		}{"sidebar-scroll", testSidebarScrollBrowser}, struct {
			name string
			fn   func(*testing.T, *rod.Browser)
		}{"message-link", testMessageLinkBrowser})
		for i := range n {
			for _, scenario := range neighbours {
				t.Run(fmt.Sprintf("%d/%s", i, scenario.name), func(t *testing.T) {
					t.Parallel()
					b := <-pool
					t.Cleanup(func() { pool <- b })
					if ctx.Err() != nil {
						t.Error("probe scheduling budget exhausted")
						return
					}
					run(t, b, scenario.fn)
				})
			}
		}
	})
	t.Errorf("PROBE RESULT\nN=%d; fresh / one reused browser per target / pool of 3 with real neighbours\n%s", n, strings.Join(results, "\n\n"))
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
