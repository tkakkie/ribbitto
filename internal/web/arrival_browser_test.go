package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

// The pill grows in and a panel slides in only on a push navigation inside
// the app that changes the place or opens the panel, never again when a live
// sidebar list replaces the pill, and never under reduced motion (#921).
func testArrivalBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cursor := int64(42)
	channels := []view.Channel{{ID: kernel.ID{1}, Name: "general"}, {ID: kernel.ID{2}, Name: "random"}, {ID: kernel.ID{3}, Name: "design"}}
	topic := view.Topic{ID: kernel.ID{4}, Name: "plans"}
	screen := func(path string) view.ChannelPage {
		page := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"}, Channels: channels, EventCursor: &cursor}
		for _, panel := range []string{view.PanelMembers, view.PanelOverview} {
			if rest, ok := strings.CutSuffix(path, "/"+panel); ok {
				path, page.Panel, page.Members = rest, panel, &view.MemberPage{}
			}
		}
		for _, c := range channels {
			if strings.HasPrefix(path, view.ChannelURL("acme", c.ID)) {
				page.Current = c
			}
		}
		if page.Current.ID == channels[0].ID {
			page.Topics = []view.Topic{topic}
			if path == view.ConversationURL("acme", channels[0].ID, &topic.ID) {
				page.Topic = &topic
			}
		}
		return page
	}
	var mu sync.Mutex
	var latest chan realtime.Outgoing
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/organizations/acme/events" {
			templ.Handler(view.ChannelScreen("/static/css/app.css", screen(r.URL.Path))).ServeHTTP(w, r)
			return
		}
		frames := make(chan realtime.Outgoing)
		mu.Lock()
		latest = frames
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
		if err := sender.Heartbeat(r.Context()); err != nil {
			return
		}
		for {
			select {
			case out := <-frames:
				if sender.Send(r.Context(), out) != nil {
					return
				}
			case <-r.Context().Done():
				return
			}
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	defer page.MustClose()
	// Animations run, and their events fire, only in a tab that repaints.
	page.MustActivate()
	page.MustEvalOnNewDocument(`window.arrivals = [];
		document.addEventListener('htmx:sseOpen', () => { window.streamOpen = true; });
		document.addEventListener('animationstart', e => { if (e.animationName.startsWith('rb-')) arrivals.push(e.animationName); }, true);`)
	// expect reports, then forgets, the arrival animations started so far.
	expect := func(step, want string) {
		t.Helper()
		// Two frames: an animation started by the last update has its event.
		page.MustEval(`() => new Promise(done => requestAnimationFrame(() => requestAnimationFrame(done)))`)
		if got := page.MustEval(`() => { const got = arrivals.join(','); arrivals.length = 0; return got; }`).Str(); got != want {
			t.Fatalf("%s: arrival animations = %q, want %q", step, got, want)
		}
	}
	// navigate waits for the page that start leads to. The browser runs
	// without the back/forward cache, so every page is a new document.
	navigate := func(start func()) {
		t.Helper()
		page.MustEval(`() => { window.leaving = true; }`)
		start()
		page.MustWait(`() => !window.leaving && document.readyState === 'complete'`)
	}
	click := func(selector string) {
		t.Helper()
		navigate(func() { page.MustElement(selector).MustClick() })
	}
	// Reload, Back and Forward start from the browser. rod's MustReload,
	// MustNavigateBack and MustNavigateForward call location.reload() or
	// history.back() in the page instead; the new document can then commit
	// in a new frame host before that call has answered, and Chrome fails
	// the call with "Inspected target navigated or closed" (#1251).
	reload := func() {
		t.Helper()
		navigate(func() {
			if err := (proto.PageReload{}).Call(page); err != nil {
				t.Fatal(err)
			}
		})
	}
	traverse := func(delta int) {
		t.Helper()
		navigate(func() {
			history, err := proto.PageGetNavigationHistory{}.Call(page)
			if err != nil {
				t.Fatal(err)
			}
			to := history.CurrentIndex + delta
			if to < 0 || to >= len(history.Entries) {
				t.Fatalf("no history entry %d from %d of %d", delta, history.CurrentIndex, len(history.Entries))
			}
			if err := (proto.PageNavigateToHistoryEntry{EntryID: history.Entries[to].ID}).Call(page); err != nil {
				t.Fatal(err)
			}
		})
	}
	link := func(id kernel.ID) string { return "#link-" + view.ChannelDOMID(id) }
	// replaceSidebar delivers a live sidebar list for the current page through
	// the stream sink and waits until it replaced the old list.
	replaceSidebar := func(path string) {
		t.Helper()
		page.MustWait(`() => window.streamOpen === true`)
		mu.Lock()
		frames := latest
		mu.Unlock()
		sidebar := screen(path)
		sidebar.ChannelCounts = map[kernel.ID]int64{channels[0].ID: 1}
		page.MustEval(`() => { document.getElementById('sidebar-channels').dataset.stale = ''; }`)
		select {
		case frames <- realtime.Outgoing{Name: "sidebar", Ephemeral: true, Data: streamTestMarkup(t, view.SidebarLists(sidebar))}:
		case <-ctx.Done():
			t.Fatal("stream did not take the sidebar", ctx.Err())
		}
		page.MustWait(`() => !('stale' in document.getElementById('sidebar-channels').dataset)`)
	}
	general, random, design := view.ChannelURL("acme", channels[0].ID), view.ChannelURL("acme", channels[1].ID), view.ChannelURL("acme", channels[2].ID)

	page.MustNavigate(server.URL + general).MustWaitLoad()
	expect("first load", "")
	click(link(channels[1].ID))
	expect("channel link", "rb-pill-in")
	reload()
	expect("reload", "")
	click(link(channels[0].ID))
	expect("channel link back", "rb-pill-in")
	click("#link-" + view.TopicDOMID(topic.ID))
	expect("topic link", "rb-pill-in")
	traverse(-1)
	expect("Back", "")
	traverse(1)
	expect("Forward", "")
	click("#panel-control-members")
	expect("panel open", "rb-panel-in")
	click("#panel-control-overview")
	expect("panel switch", "")
	click("#panel-close")
	expect("panel close", "")

	// A live list after the arrival shows its pill at rest.
	click(link(channels[1].ID))
	expect("channel link before a later list", "rb-pill-in")
	page.MustWait(`() => !('arrivePill' in document.documentElement.dataset)`)
	replaceSidebar(random)
	expect("sidebar after the arrival", "")

	// Slowed animations keep the pill growing while a live list arrives.
	if err := (proto.AnimationEnable{}).Call(page); err != nil {
		t.Fatal(err)
	}
	if err := (proto.AnimationSetPlaybackRate{PlaybackRate: 0.01}).Call(page); err != nil {
		t.Fatal(err)
	}
	click(link(channels[2].ID))
	page.MustWait(`() => arrivals.includes('rb-pill-in') && 'arrivePill' in document.documentElement.dataset`)
	replaceSidebar(design)
	expect("sidebar within the arrival", "rb-pill-in")
	if err := (proto.AnimationSetPlaybackRate{PlaybackRate: 1}).Call(page); err != nil {
		t.Fatal(err)
	}

	reduce := proto.EmulationSetEmulatedMedia{Features: []*proto.EmulationMediaFeature{{Name: "prefers-reduced-motion", Value: "reduce"}}}
	if err := reduce.Call(page); err != nil {
		t.Fatal(err)
	}
	click(link(channels[0].ID))
	expect("channel link, reduced motion", "")
	click("#panel-control-members")
	expect("panel open, reduced motion", "")
}
