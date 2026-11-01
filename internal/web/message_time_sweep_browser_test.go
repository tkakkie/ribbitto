package web

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

// Localising message times below desk must not move any message row at
// any width, font or text size (#2044). Whether a time's rewrite moves the
// rows depends on whether the server's UTC text and the local text wrap the
// header differently, which happens only in a band of widths that depends
// on the fonts the runner has. So besides a fixed sweep of phone widths in
// both languages at 100 % and 125 % text, and 200 % text at 320 px (where
// no time may reach past the list's edge or widen what scrolls) and around
// 425 px, the critical case computes the band
// for this browser's fonts from the measured texts and loads the page
// inside it, which fails without the fix whatever font the runner uses.
// Each case holds the time script until the UTC markup has painted, then
// compares every message row's box before and after the rewrite; at 390 px
// and 100 % the header must also fit one 44 px row from the first layout
// (#1093). A message added later through htmx:load gets the same row.
func testMessageTimeSweepBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 240*time.Second)
	defer cancel()
	page := seedMessagePage(t)
	files := http.FileServerFS(static.FS())
	var hold atomic.Bool
	requested := make(chan chan struct{})
	held := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hold.Load() && strings.HasPrefix(r.URL.Path, "dist/message-time-") {
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
	mux := messagePageMux(t, page, held)
	live := page.Messages[len(page.Messages)-1]
	live.ID, live.EventSeq = kernel.ID{0x7f}, live.EventSeq+1
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("GET /live-message", catalogues.Middleware(templ.Handler(view.LiveMessageItem(live))))
	server := httptest.NewServer(mux)
	defer server.Close()
	tab, closeTab := boundedPage(t, browser, ctx, "message time sweep")
	defer closeTab()
	step := pageStep(ctx, tab)
	defer deleteLangCookie(t, tab, server.URL)
	n := len(page.Messages)
	step(t, "preparing the page", func(p *rod.Page) {
		p.MustActivate()
		if err := (proto.NetworkEnable{}).Call(p); err != nil {
			panic(err)
		}
		if err := (proto.NetworkSetCacheDisabled{CacheDisabled: true}).Call(p); err != nil {
			panic(err)
		}
		if err := (proto.EmulationSetTimezoneOverride{TimezoneID: "Asia/Tokyo"}).Call(p); err != nil {
			panic(err)
		}
	})
	// load opens the page at width×height with the browser's default text
	// size scaled by scale and the time script held, and returns the rows
	// once the server's UTC times have painted.
	load := func(t *testing.T, width, height int, lang string, scale float64) (rows messageRows, release func()) {
		t.Helper()
		hold.Store(true)
		step(t, "loading the page with the time script held", func(p *rod.Page) {
			if err := (proto.EmulationSetDeviceMetricsOverride{Width: width, Height: height, DeviceScaleFactor: 1}).Call(p); err != nil {
				panic(err)
			}
			size := int(math.Round(16 * scale))
			if err := (proto.PageSetFontSizes{FontSizes: &proto.PageFontSizes{Standard: &size}}).Call(p); err != nil {
				panic(err)
			}
			if err := p.SetCookies([]*proto.NetworkCookieParam{{Name: "lang", Value: lang, URL: server.URL}}); err != nil {
				panic(err)
			}
			if _, err := (proto.PageNavigate{URL: server.URL + "/"}).Call(p); err != nil {
				panic(err)
			}
		})
		var gate chan struct{}
		select {
		case gate = <-requested:
		case <-time.After(10 * time.Second):
			t.Fatal("the page did not request the time script within 10 s")
		}
		step(t, "painting the server's times", func(p *rod.Page) {
			p.MustWait(`n => document.querySelectorAll('#message-items time[data-local-time]').length === n &&
				performance.getEntriesByType('paint').some(e => e.name === 'first-contentful-paint')`, n)
			p.MustEval(`() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))`)
			rows = readMessageRows(p)
		})
		for _, row := range rows.Items {
			if !strings.Contains(row.Time, "UTC") {
				t.Fatalf("%s was localised before the server's markup painted: %q", row.ID, row.Time)
			}
		}
		return rows, func() {
			close(gate)
			hold.Store(false)
		}
	}
	// localised releases the time script and returns the rows once every
	// time is local.
	localised := func(t *testing.T, release func()) (rows messageRows) {
		t.Helper()
		release()
		step(t, "localising the times", func(p *rod.Page) {
			p.MustWait(`() => [...document.querySelectorAll('time[data-local-time]')].every(e => !e.textContent.includes('UTC'))`)
			p.MustEval(`() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))`)
			rows = readMessageRows(p)
		})
		return rows
	}
	check := func(t *testing.T, what string, before, after messageRows, header44 bool) {
		t.Helper()
		for _, problem := range compareMessageRows(before, after) {
			t.Errorf("%s: localising the times moved a row: %s", what, problem)
		}
		if after.ListScrollWidth > before.ListScrollWidth+0.5 || after.PageScrollWidth > before.PageScrollWidth+0.5 {
			t.Errorf("%s: localising the times widened the scrolled content: %s → %s", what, before.Widths, after.Widths)
		}
		for _, state := range []struct {
			name string
			rows messageRows
		}{{"server's UTC times", before}, {"local times", after}} {
			for _, overflow := range state.rows.TimeOverflow {
				t.Errorf("%s: with the %s a time reaches past the list's edge: %s (%s)", what, state.name, overflow, state.rows.Widths)
			}
			if state.rows.SidewaysScroll {
				// Not from the time (checked above): at 200 % text a
				// header's Select label alone can be wider than a 320 px
				// phone's group (reported separately from #2044).
				t.Logf("%s: with the %s the list scrolls sideways, not because of a time: %s", what, state.name, state.rows.Widths)
			}
			if !header44 {
				continue
			}
			for _, row := range state.rows.Items {
				if row.HeaderHeight > 44.5 {
					t.Errorf("%s: with the %s %s's header takes more than one 44 px row: height=%g (%s)", what, state.name, row.ID, row.HeaderHeight, state.rows.Widths)
				}
			}
		}
	}
	type sweepCase struct {
		width, height int
		lang          string
		scale         float64
	}
	var cases []sweepCase
	for _, scale := range []float64{1, 1.25} {
		for _, width := range []int{365, 370, 375, 380, 385, 390} {
			for _, lang := range []string{"en", "ja"} {
				cases = append(cases, sweepCase{width, 844, lang, scale})
			}
		}
	}
	for _, width := range []int{320, 420, 425, 430} {
		for _, lang := range []string{"en", "ja"} {
			cases = append(cases, sweepCase{width, 844, lang, 2})
		}
	}
	for _, c := range cases {
		name := fmt.Sprintf("%dx%d/text=%d%%/%s", c.width, c.height, int(c.scale*100), c.lang)
		t.Run(name, func(t *testing.T) {
			before, release := load(t, c.width, c.height, c.lang, c.scale)
			after := localised(t, release)
			check(t, name, before, after, c.width == 390 && c.scale == 1)
		})
	}
	// The critical band: from the texts' own widths, find a viewport where
	// the header fits the local time beside the topic label and Select but
	// not the server's UTC time, in a viewport tall enough that neither
	// layout needs a scrollbar. Without a reserved time box, localising there
	// always unwraps every header.
	for _, lang := range []string{"en", "ja"} {
		t.Run("critical/"+lang, func(t *testing.T) {
			const height = 1600
			var need struct {
				Inset, UTC, Local float64
			}
			_, release := load(t, 600, height, lang, 1)
			step(t, "measuring the header's needs", func(p *rod.Page) {
				if err := p.MustEval(messageHeaderNeeds).Unmarshal(&need); err != nil {
					panic(err)
				}
			})
			release()
			if need.UTC-need.Local < 2 {
				t.Fatalf("the UTC header needs %g px and the local one %g px: no band to test in this browser's fonts", need.UTC, need.Local)
			}
			width := int(math.Ceil(need.Inset + (need.UTC+need.Local)/2))
			what := fmt.Sprintf("%dx%d/%s (UTC header needs %.1f px, local %.1f px)", width, height, lang, need.UTC, need.Local)
			before, release := load(t, width, height, lang, 1)
			after := localised(t, release)
			check(t, what, before, after, false)
		})
	}
	// Without JavaScript the server's time shows once: the copy that keeps
	// its box below desk is laid out but never painted or read.
	t.Run("no-javascript", func(t *testing.T) {
		defer step(t, "enabling JavaScript again", func(p *rod.Page) {
			if err := (proto.EmulationSetScriptExecutionDisabled{Value: false}).Call(p); err != nil {
				panic(err)
			}
		})
		for _, c := range []struct {
			width int
			scale float64
		}{{390, 1}, {320, 2}, {1440, 1}} {
			step(t, fmt.Sprintf("loading the page at %d px and %d %% text without JavaScript", c.width, int(c.scale*100)), func(p *rod.Page) {
				if err := (proto.EmulationSetScriptExecutionDisabled{Value: true}).Call(p); err != nil {
					panic(err)
				}
				if err := (proto.EmulationSetDeviceMetricsOverride{Width: c.width, Height: 844, DeviceScaleFactor: 1}).Call(p); err != nil {
					panic(err)
				}
				size := int(math.Round(16 * c.scale))
				if err := (proto.PageSetFontSizes{FontSizes: &proto.PageFontSizes{Standard: &size}}).Call(p); err != nil {
					panic(err)
				}
				if err := navigateLoaded(p, server.URL+"/"); err != nil {
					panic(err)
				}
				for _, problem := range p.MustEval(`want => [...document.querySelectorAll('#message-items time[data-local-time]')].flatMap(time => {
					const column = time.parentElement, name = column.firstElementChild, copy = getComputedStyle(column, '::after');
					const problems = [], h = e => e.getBoundingClientRect().height;
					if (time.textContent !== want || time.innerText !== want || column.innerText.includes(want + want)) problems.push('reads ' + JSON.stringify(column.innerText) + ', want the time ' + JSON.stringify(want) + ' once');
					// Below desk the copy shares the time's grid cell, so the
					// column is the name's row and the time's; from desk the
					// column is display: contents and has no copy.
					if (innerWidth < 852 && Math.abs(h(column) - h(name) - h(time)) > 0.5) problems.push('the copy takes room of its own: column ' + h(column) + ', name ' + h(name) + ', time ' + h(time));
					if (copy.content !== 'none' && copy.visibility !== 'hidden') problems.push('the server text copy is visible');
					return problems.map(p => time.closest('li').id + ': ' + p);
				})`, "2026-12-28 13:58:58 UTC").Arr() {
					t.Errorf("width=%d text=%d%%: without JavaScript %s", c.width, int(c.scale*100), problem.Str())
				}
			})
		}
	})
	t.Run("htmx-load", func(t *testing.T) {
		// Tall enough that the added row brings no scrollbar, which would
		// narrow every row for a reason other than its time.
		_, release := load(t, 390, 1600, "en", 1)
		first := localised(t, release)
		var added messageRows
		step(t, "adding a message through htmx", func(p *rod.Page) {
			p.MustEval(`async () => {
				const html = await (await fetch('/live-message')).text();
				htmx.swap('#message-items', html, {swapStyle: 'beforeend'});
				await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)));
			}`)
			added = readMessageRows(p)
		})
		if len(added.Items) != len(first.Items)+1 {
			t.Fatalf("rows after the swap: %d, want %d", len(added.Items), len(first.Items)+1)
		}
		last, twin := added.Items[len(added.Items)-1], first.Items[len(first.Items)-1]
		if strings.Contains(last.Time, "UTC") {
			t.Fatalf("the added message kept the server's time %q", last.Time)
		}
		if math.Abs(last.Width-twin.Width) > 0.5 || math.Abs(last.Height-twin.Height) > 0.5 || math.Abs(last.HeaderHeight-twin.HeaderHeight) > 0.5 {
			t.Errorf("the added message's row is %s, its twin on the page %s", last.box(), twin.box())
		}
		for _, problem := range compareMessageRows(first, messageRows{Items: added.Items[:len(first.Items)], Widths: added.Widths}) {
			t.Errorf("adding a message moved an existing row: %s", problem)
		}
	})
}

// messageRows is every message row's box, read by readMessageRows, with
// the page's and the list's scrolled widths and each time that reaches past
// the list's content edge.
type messageRows struct {
	Items                            []messageRow
	SidewaysScroll                   bool
	PageScrollWidth, ListScrollWidth float64
	TimeOverflow                     []string
	Widths                           string
}

// messageRow is one message item's box, scrolled into list coordinates, and
// its time's text.
type messageRow struct {
	ID, Time                          string
	X, Y, Width, Height, HeaderHeight float64
}

func (r messageRows) find(id string) int {
	for i, item := range r.Items {
		if item.ID == id {
			return i
		}
	}
	return -1
}

func (item messageRow) box() string {
	return fmt.Sprintf("(%.1f, %.1f) %.1f×%.1f, header %.1f", item.X, item.Y, item.Width, item.Height, item.HeaderHeight)
}

func readMessageRows(p *rod.Page) messageRows {
	var rows messageRows
	if err := p.MustEval(`() => {
		const list = document.getElementById('message-list'), doc = document.documentElement;
		const items = [...document.querySelectorAll('#message-items > li')].map(li => {
			const r = li.getBoundingClientRect(), header = li.firstElementChild.getBoundingClientRect();
			return {id: li.id, time: li.querySelector('time').textContent, x: r.x, y: r.y + list.scrollTop, width: r.width, height: r.height, headerHeight: header.height};
		});
		const edge = list.getBoundingClientRect().left + list.clientWidth;
		const timeOverflow = [...document.querySelectorAll('#message-items time')].filter(e => e.getBoundingClientRect().right > edge + 0.5)
			.map(e => e.closest('li').id + ' time right=' + Math.round(e.getBoundingClientRect().right * 10) / 10 + ' edge=' + edge);
		return {items, timeOverflow, pageScrollWidth: doc.scrollWidth, listScrollWidth: list.scrollWidth,
			sidewaysScroll: doc.scrollWidth > doc.clientWidth || list.scrollWidth > list.clientWidth,
			widths: 'viewport=' + innerWidth + ' page=' + doc.scrollWidth + '/' + doc.clientWidth + ' list=' + list.scrollWidth + '/' + list.clientWidth};
	}`).Unmarshal(&rows); err != nil {
		panic(err)
	}
	return rows
}

// compareMessageRows lists each row whose box changed by more than half a
// pixel between before and after.
func compareMessageRows(before, after messageRows) []string {
	var problems []string
	for _, b := range before.Items {
		i := after.find(b.ID)
		if i < 0 {
			problems = append(problems, b.ID+" is gone")
			continue
		}
		a := after.Items[i]
		if math.Abs(a.X-b.X) > 0.5 || math.Abs(a.Y-b.Y) > 0.5 || math.Abs(a.Width-b.Width) > 0.5 || math.Abs(a.Height-b.Height) > 0.5 {
			problems = append(problems, fmt.Sprintf("%s %s → %s (%s → %s)", b.ID, b.box(), a.box(), before.Widths, after.Widths))
		}
	}
	return problems
}

// messageHeaderNeeds measures, on a page wide enough for every header to
// fit one row, the width each header needs with the server's UTC time and
// with the local time the script will write, from the texts themselves
// rather than the time's box, plus the viewport width the header does not
// get. The local text is formatted and measured as message-time.ts writes
// it, in a probe with the time's font.
const messageHeaderNeeds = `() => {
	const formatter = new Intl.DateTimeFormat(document.documentElement.lang, {
		year: "numeric", month: "numeric", day: "numeric",
		hour: "numeric", minute: "numeric", second: "numeric",
	});
	const textWidth = (element, text) => {
		const probe = document.createElement('span');
		probe.style.font = getComputedStyle(element).font;
		probe.style.whiteSpace = 'nowrap';
		probe.style.position = 'absolute';
		probe.textContent = text;
		document.body.append(probe);
		const width = probe.getBoundingClientRect().width;
		probe.remove();
		return width;
	};
	let utc = 0, local = 0, inset = 0;
	for (const li of document.querySelectorAll('#message-items > li')) {
		const header = li.firstElementChild, column = header.children[0], group = header.children[1];
		const name = column.children[0], time = li.querySelector('time');
		const gap = parseFloat(getComputedStyle(header).columnGap) || 0;
		const range = document.createRange();
		range.selectNodeContents(name);
		const nameWidth = range.getBoundingClientRect().width;
		const rest = gap + group.getBoundingClientRect().width;
		utc = Math.max(utc, Math.max(nameWidth, textWidth(time, time.textContent)) + rest);
		local = Math.max(local, Math.max(nameWidth, textWidth(time, formatter.format(new Date(time.dateTime)))) + rest);
		inset = innerWidth - header.getBoundingClientRect().width;
	}
	return {inset, utc, local};
}`
