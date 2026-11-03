package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

// Without JavaScript the latest conversation page opens at its newest entry
// through CSS alone, while short or empty pages, older pages, permalinks and
// the JavaScript path keep their positions (#1489).
func testMessageListWithoutJavaScript(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	topic, cursor := kernel.ID{7}, int64(99)
	seqs := make([]int, 90)
	for i := range seqs {
		seqs[i] = i
	}
	latest := view.ChannelPage{
		Organization: view.Organization{Name: "Acme", Slug: "acme"},
		Current:      view.Channel{ID: kernel.ID{1}, Name: "general"},
		Topic:        &view.Topic{ID: topic, Name: "plans"},
		EventCursor:  &cursor,
		Reading:      true,
		Older:        true,
		Messages:     streamTestMessages(topic, seqs[40:]...),
	}
	few := latest
	few.Older, few.Messages = false, streamTestMessages(topic, 1, 2, 3)
	empty := latest
	empty.Older, empty.Messages = false, nil
	before := latest
	before.Before, before.EventCursor, before.Older, before.Messages = 40, nil, true, streamTestMessages(topic, seqs[5:55]...)
	around := latest
	around.Around, around.EventCursor, around.Older, around.Messages = 30, nil, true, streamTestMessages(topic, seqs[5:55]...)
	pages := map[string]view.ChannelPage{
		"/latest": latest,
		"/few":    few,
		"/empty":  empty,
		"/before": before,
		"/around": around,
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("/organizations/acme/events", idleStream)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		page, ok := pages[r.URL.Path]
		if !ok && r.URL.Query().Get("before") != "" {
			page, ok = before, true
		}
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		templ.Handler(view.ChannelScreen("/static/css/app.css", page)).ServeHTTP(w, r)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	noJSTab, closeNoJSTab := boundedPage(t, browser, ctx, "message-list without JavaScript")
	defer closeNoJSTab()
	noJS := noJSTab.Context(ctx).WithPanic(failAtTestLine(t))
	if err := (proto.EmulationSetScriptExecutionDisabled{Value: true}).Call(noJS); err != nil {
		t.Fatal(err)
	}
	requireScriptingNone := func(t *testing.T, p *rod.Page) {
		t.Helper()
		if !p.MustEval(`() => matchMedia('(scripting: none)').matches`).Bool() {
			t.Fatal("(scripting: none) did not match")
		}
	}
	const geomJS = `() => {
		const list = document.getElementById('message-list');
		const port = list.getBoundingClientRect();
		const items = [...document.querySelectorAll('#message-items > li')];
		const inside = e => {
			if (!e) return false;
			const r = e.getBoundingClientRect();
			return r.top >= port.top && r.bottom <= port.bottom;
		};
		return {
			firstInView: inside(items[0]),
			lastInView: inside(items.at(-1)),
			active: document.activeElement?.tagName.toLowerCase() || '',
			scrollTop: list.scrollTop,
			listHeight: port.height,
		};
	}`

	for _, c := range []struct {
		name   string
		width  int
		height int
		mobile bool
		short  bool
	}{
		{"width=1280/latest", 1280, 800, false, false},
		{"width=390/latest", 390, 844, true, false},
		{"height=400/latest", 1280, 400, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			noJS.MustSetViewport(c.width, c.height, 1, c.mobile)
			if err := navigateLoaded(noJS, server.URL+"/latest"); err != nil {
				t.Fatal(err)
			}
			requireScriptingNone(t, noJS)
			g := noJS.MustEval(geomJS)
			if !g.Get("lastInView").Bool() || g.Get("firstInView").Bool() || g.Get("active").Str() != "body" {
				t.Fatalf("lastInView=%t firstInView=%t active=%q, want lastInView=true firstInView=false active=\"body\"",
					g.Get("lastInView").Bool(), g.Get("firstInView").Bool(), g.Get("active").Str())
			}
			if c.short {
				shortState := noJS.MustEval(`() => {
					const conv = document.getElementById('conversation'), convBox = conv.getBoundingClientRect();
					const h1 = conv.querySelector('h1').getBoundingClientRect();
					return {
						convScrollTop: conv.scrollTop,
						h1InView: h1.top >= convBox.top && h1.bottom <= convBox.bottom && h1.bottom <= innerHeight,
					};
				}`)
				if g.Get("listHeight").Num() != 128 || shortState.Get("convScrollTop").Num() != 0 || !shortState.Get("h1InView").Bool() {
					t.Fatalf("listHeight=%v convScrollTop=%v h1InView=%t, want 128, 0, true",
						g.Get("listHeight").Num(), shortState.Get("convScrollTop").Num(), shortState.Get("h1InView").Bool())
				}
			}
		})
	}

	t.Run("width=1280/back", func(t *testing.T) {
		noJS.MustSetViewport(1280, 800, 1, false)
		if err := navigateLoaded(noJS, server.URL+"/latest"); err != nil {
			t.Fatal(err)
		}
		requireScriptingNone(t, noJS)
		noJS.MustEval(`() => { window.leaving = true; document.querySelector('#load-older a').click(); }`)
		mustWaitAcrossNavigation(t, noJS, `() => !window.leaving && document.readyState === 'complete'`)
		noJS.MustEval(`() => { window.leaving = true; }`)
		history, err := proto.PageGetNavigationHistory{}.Call(noJS)
		if err != nil {
			t.Fatal(err)
		}
		if history.CurrentIndex < 1 {
			t.Fatalf("history index = %d, want at least 1", history.CurrentIndex)
		}
		if err := (proto.PageNavigateToHistoryEntry{EntryID: history.Entries[history.CurrentIndex-1].ID}).Call(noJS); err != nil {
			t.Fatal(err)
		}
		mustWaitAcrossNavigation(t, noJS, `() => !window.leaving && document.readyState === 'complete'`)
		requireScriptingNone(t, noJS)
		g := noJS.MustEval(geomJS)
		if !g.Get("lastInView").Bool() || g.Get("firstInView").Bool() || g.Get("active").Str() != "body" {
			t.Fatalf("lastInView=%t firstInView=%t active=%q, want lastInView=true firstInView=false active=\"body\"",
				g.Get("lastInView").Bool(), g.Get("firstInView").Bool(), g.Get("active").Str())
		}
	})

	t.Run("width=1280/few", func(t *testing.T) {
		noJS.MustSetViewport(1280, 800, 1, false)
		if err := navigateLoaded(noJS, server.URL+"/few"); err != nil {
			t.Fatal(err)
		}
		requireScriptingNone(t, noJS)
		diff := noJS.MustEval(`() => {
			const list = document.getElementById('message-list'), items = document.getElementById('message-items');
			const first = items.querySelector('li');
			const want = list.getBoundingClientRect().top + parseFloat(getComputedStyle(items).paddingTop);
			return Math.abs(first.getBoundingClientRect().top - want);
		}`).Num()
		if diff > 1 {
			t.Fatalf("first item top differs from list top + paddingTop by %v px, want <= 1 px", diff)
		}
	})

	t.Run("width=1280/empty", func(t *testing.T) {
		noJS.MustSetViewport(1280, 800, 1, false)
		if err := navigateLoaded(noJS, server.URL+"/empty"); err != nil {
			t.Fatal(err)
		}
		requireScriptingNone(t, noJS)
		res := noJS.MustEval(`() => {
			const list = document.getElementById('message-list').getBoundingClientRect();
			const empty = document.querySelector('#message-empty > p');
			const r = empty.getBoundingClientRect();
			return {
				visible: empty.checkVisibility() && r.width > 0 && r.height > 0,
				offset: Math.abs((r.top + r.bottom) / 2 - (list.top + list.bottom) / 2),
			};
		}`)
		if !res.Get("visible").Bool() || res.Get("offset").Num() > 1 {
			t.Fatalf("empty visible=%t centre offset=%v px, want visible=true offset<=1 px",
				res.Get("visible").Bool(), res.Get("offset").Num())
		}
	})

	t.Run("width=1280/permalink-oldest", func(t *testing.T) {
		noJS.MustSetViewport(1280, 800, 1, false)
		target := view.MessageDOMID(kernel.ID{40})
		if err := navigateLoaded(noJS, server.URL+"/latest#"+target); err != nil {
			t.Fatal(err)
		}
		requireScriptingNone(t, noJS)
		res := noJS.MustEval(`id => {
			const list = document.getElementById('message-list').getBoundingClientRect();
			const item = document.getElementById(id), r = item.getBoundingClientRect();
			const last = document.querySelector('#message-items > li:last-child').getBoundingClientRect();
			return {
				inView: r.top >= list.top && r.bottom <= list.bottom,
				isTarget: item.matches(':target'),
				newestInView: last.top >= list.top && last.bottom <= list.bottom,
			};
		}`, target)
		if !res.Get("inView").Bool() || !res.Get("isTarget").Bool() || res.Get("newestInView").Bool() {
			t.Fatalf("inView=%t isTarget=%t newestInView=%t, want true, true, false",
				res.Get("inView").Bool(), res.Get("isTarget").Bool(), res.Get("newestInView").Bool())
		}
	})

	for _, name := range []string{"before", "around"} {
		t.Run("width=1280/"+name, func(t *testing.T) {
			noJS.MustSetViewport(1280, 800, 1, false)
			if err := navigateLoaded(noJS, server.URL+"/"+name); err != nil {
				t.Fatal(err)
			}
			requireScriptingNone(t, noJS)
			g := noJS.MustEval(geomJS)
			if !g.Get("firstInView").Bool() || g.Get("lastInView").Bool() || g.Get("scrollTop").Num() != 0 {
				t.Fatalf("firstInView=%t lastInView=%t scrollTop=%v, want true, false, 0",
					g.Get("firstInView").Bool(), g.Get("lastInView").Bool(), g.Get("scrollTop").Num())
			}
		})
	}

	t.Run("scripts=true/width=1280/latest", func(t *testing.T) {
		jsTab, closeJSTab := boundedPage(t, browser, ctx, "message-list with JavaScript")
		defer closeJSTab()
		jsPage := jsTab.Context(ctx).WithPanic(failAtTestLine(t))
		jsPage.MustSetViewport(1280, 800, 1, false)
		jsPage.MustNavigate(server.URL + "/latest").MustWaitLoad()
		res := jsPage.MustEval(`() => {
			const list = document.getElementById('message-list');
			return {
				scriptingNone: matchMedia('(scripting: none)').matches,
				display: getComputedStyle(list).display,
			};
		}`)
		g := jsPage.MustEval(geomJS)
		if res.Get("scriptingNone").Bool() || res.Get("display").Str() != "block" || !g.Get("lastInView").Bool() || g.Get("firstInView").Bool() {
			t.Fatalf("scriptingNone=%t display=%q lastInView=%t firstInView=%t, want false, \"block\", true, false",
				res.Get("scriptingNone").Bool(), res.Get("display").Str(), g.Get("lastInView").Bool(), g.Get("firstInView").Bool())
		}
	})
}
