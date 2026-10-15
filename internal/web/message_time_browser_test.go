package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/web/static"
)

// Localising a message's time moves nothing on its row (#1094). The time
// script is held until the server's UTC markup has painted, as on a slow
// connection, and the layout shifts with a source inside a message row are
// summed from navigation until 2 s after localisation, at desktop and phone
// width in both languages. Without JavaScript the server's time stays.
func testMessageTimeBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	page := seedMessagePage(t)
	files := http.FileServerFS(static.FS())
	var hold atomic.Bool
	// Each held request of the time script sends its release.
	requested := make(chan chan struct{})
	held := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hold.Load() && strings.HasPrefix(r.URL.Path, "message-time-") {
			release := make(chan struct{})
			select {
			case requested <- release:
			case <-r.Context().Done():
				return
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		files.ServeHTTP(w, r)
	})
	server := httptest.NewServer(messagePageMux(t, page, held))
	defer server.Close()
	tab, closeTab := boundedPage(t, browser, ctx, "message times")
	defer closeTab()
	step := pageStep(ctx, tab)
	defer deleteLangCookie(t, tab, server.URL)
	n := len(page.Messages)
	step(t, "preparing the page", func(p *rod.Page) {
		p.MustActivate()
		// A cached script would skip the hold.
		if err := (proto.NetworkEnable{}).Call(p); err != nil {
			panic(err)
		}
		if err := (proto.NetworkSetCacheDisabled{CacheDisabled: true}).Call(p); err != nil {
			panic(err)
		}
		if err := (proto.EmulationSetTimezoneOverride{TimezoneID: "Asia/Tokyo"}).Call(p); err != nil {
			panic(err)
		}
		p.MustEvalOnNewDocument(`window.rowShifts = [];
			new PerformanceObserver(list => window.rowShifts.push(...list.getEntries())).observe({type: 'layout-shift', buffered: true});`)
	})
	for _, size := range [][2]int{{1440, 900}, {390, 844}} {
		for _, lang := range []string{"en", "ja"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size[0], size[1], lang), func(t *testing.T) {
				hold.Store(true)
				defer hold.Store(false)
				step(t, "loading the page with the time script held", func(p *rod.Page) {
					if err := (proto.EmulationSetDeviceMetricsOverride{Width: size[0], Height: size[1], DeviceScaleFactor: 1}).Call(p); err != nil {
						panic(err)
					}
					if err := p.SetCookies([]*proto.NetworkCookieParam{{Name: "lang", Value: lang, URL: server.URL}}); err != nil {
						panic(err)
					}
					// The held script delays the load event, so only the
					// navigation is awaited.
					if _, err := (proto.PageNavigate{URL: server.URL + "/"}).Call(p); err != nil {
						panic(err)
					}
				})
				var release chan struct{}
				select {
				case release = <-requested:
				case <-time.After(10 * time.Second):
					t.Fatal("the page did not request the time script within 10 s")
				}
				step(t, "painting the server's times", func(p *rod.Page) {
					p.MustWait(`n => document.querySelectorAll('#message-items time[data-local-time]').length === n &&
						performance.getEntriesByType('paint').some(e => e.name === 'first-contentful-paint')`, n)
					p.MustEval(`() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))`)
					if localised := p.MustEval(`() => [...document.querySelectorAll('time[data-local-time]')].filter(e => !e.textContent.includes('UTC')).length`).Int(); localised != 0 {
						panic(fmt.Sprintf("%d times were localised before the server's markup painted", localised))
					}
				})
				close(release)
				var shifts struct {
					Sum     float64
					Entries []string
				}
				step(t, "localising the times and waiting 2 s", func(p *rod.Page) {
					p.MustWait(`() => [...document.querySelectorAll('time[data-local-time]')].every(e => !e.textContent.includes('UTC'))`)
					if err := p.MustEval(messageRowShifts).Unmarshal(&shifts); err != nil {
						panic(err)
					}
				})
				if shifts.Sum != 0 {
					t.Errorf("width=%d language=%s: localising the times shifted message rows by %g: %s", size[0], lang, shifts.Sum, strings.Join(shifts.Entries, "; "))
				}
			})
		}
	}
	t.Run("no-javascript", func(t *testing.T) {
		step(t, "loading the page without JavaScript", func(p *rod.Page) {
			if err := (proto.EmulationSetScriptExecutionDisabled{Value: true}).Call(p); err != nil {
				panic(err)
			}
			if err := navigateLoaded(p, server.URL+"/"); err != nil {
				panic(err)
			}
		})
		step(t, "reading the server's times without JavaScript", func(p *rod.Page) {
			var stamps []struct {
				Text  string
				Width float64
			}
			if err := p.MustEval(`() => [...document.querySelectorAll('#message-items time[data-local-time]')].map(e => ({text: e.textContent, width: e.getBoundingClientRect().width}))`).Unmarshal(&stamps); err != nil {
				panic(err)
			}
			if len(stamps) != n {
				panic(fmt.Sprintf("rendered %d times, want %d", len(stamps), n))
			}
			for _, stamp := range stamps {
				if stamp.Text != "2026-12-28 13:58:58 UTC" || stamp.Width == 0 {
					panic(fmt.Sprintf("without JavaScript a time shows %q at width %g, want the server's UTC time", stamp.Text, stamp.Width))
				}
			}
		})
	})
}

// messageRowShifts waits 2 s after localisation, then sums the recorded
// layout shifts without recent input and with a source inside a message row,
// and describes each.
const messageRowShifts = `async () => {
	const localised = performance.now();
	await new Promise(r => setTimeout(r, 2000));
	const inRow = n => { const e = n && (n.nodeType === 1 ? n : n.parentElement); return !!e?.closest('#message-items > li'); };
	let sum = 0;
	const entries = [];
	for (const e of window.rowShifts) {
		if (e.hadRecentInput || e.startTime > localised + 2000) continue;
		const rows = e.sources.filter(s => inRow(s.node));
		if (rows.length === 0) continue;
		sum += e.value;
		entries.push(e.value.toPrecision(3) + ' at ' + Math.round(e.startTime) + ' ms from ' + rows.map(s => {
			const n = s.node.nodeType === 1 ? s.node : s.node.parentElement;
			return n.tagName.toLowerCase() + ' ' + JSON.stringify(s.node.textContent.trim().slice(0, 24)) + ' x ' + s.previousRect.x + ' → ' + s.currentRect.x;
		}).join(', '));
	}
	return {sum, entries};
}`
