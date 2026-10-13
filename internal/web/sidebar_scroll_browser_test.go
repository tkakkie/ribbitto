package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

// A long sidebar shows the current row on load, the selected topic or, when
// it is unlisted, its channel; a live replacement leaves the member's scroll
// position alone (#922).
func testSidebarScrollBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cursor := int64(42)
	var channels []view.Channel
	for i := range 60 {
		channels = append(channels, view.Channel{ID: kernel.ID{1, byte(i)}, Name: fmt.Sprintf("channel-%02d", i)})
	}
	var topics []view.Topic
	for i := range 51 {
		topics = append(topics, view.Topic{ID: kernel.ID{2, byte(i)}, Name: fmt.Sprintf("topic-%02d", i)})
	}
	// The current channel and its last listed topic sit near the bottom; the
	// 51st topic is outside the 50-topic list.
	current, listed, outside := channels[55], topics[:50], topics[50]
	pageFor := func(selected view.Topic, list []view.Topic) view.ChannelPage {
		return view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"},
			Current: current, Channels: channels, Topics: list, Topic: &selected, EventCursor: &cursor}
	}
	// Only the listed page streams, so the test's frame has one recipient.
	unlistedPage := pageFor(outside, listed)
	unlistedPage.EventCursor = nil
	pages := map[string]view.ChannelPage{"/listed": pageFor(topics[49], listed), "/unlisted": unlistedPage}
	connected := make(chan struct{}, 1)
	frames := make(chan realtime.Outgoing)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("/organizations/acme/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
		if err := sender.Heartbeat(r.Context()); err != nil {
			return
		}
		select {
		case connected <- struct{}{}:
		default:
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
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		page, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		templ.Handler(view.ChannelScreen("/static/css/app.css", page)).ServeHTTP(w, r)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	defer page.MustClose()
	page.MustSetViewport(1280, 600, 1, false)
	// The row is wholly inside the sidebar's scrollport, which moved to show
	// it, while focus stayed where it was.
	assertShown := func(row string) {
		t.Helper()
		if !page.MustEval(`id => {
			const pane = document.getElementById('sidebar-channels').closest('aside');
			const port = pane.getBoundingClientRect(), box = document.getElementById('link-' + id).getBoundingClientRect();
			return pane.scrollTop > 0 && box.top >= port.top && box.bottom <= port.bottom &&
				document.activeElement === document.body;
		}`, row).Bool() {
			t.Fatalf("%s is not shown in the sidebar on load, or focus moved", row)
		}
	}
	for _, c := range []struct{ path, row string }{
		{"/unlisted", view.ChannelDOMID(current.ID)},
		{"/listed", view.TopicDOMID(topics[49].ID)},
	} {
		page.MustNavigate(server.URL + c.path).MustWaitLoad()
		assertShown(c.row)
	}
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("stream did not connect", ctx.Err())
	}
	// The member scrolls away from the selection; then a new topic sorts
	// into the list and pushes the selected one, the last, out of it.
	displaced := append([]view.Topic{topics[0], {ID: kernel.ID{3}, Name: "topic-00a"}}, topics[1:49]...)
	page.MustEval(`() => {
		document.getElementById('sidebar-channels').closest('aside').scrollTop = 300;
		window.previousList = document.getElementById('sidebar-topics');
	}`)
	select {
	case frames <- realtime.Outgoing{ID: 43, Name: "sidebar", Data: streamTestMarkup(t, view.SidebarLists(pageFor(topics[49], displaced)))}:
	case <-ctx.Done():
		t.Fatal("stream did not take the frame", ctx.Err())
	}
	page.MustWait(`() => document.getElementById('sidebar-topics') !== window.previousList && !document.querySelector('.htmx-settling')`)
	// Wait out any frame-deferred work before reading the position.
	page.MustEval(`() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))`)
	if got := page.MustEval(`() => document.getElementById('sidebar-channels').closest('aside').scrollTop`).Int(); got != 300 {
		t.Fatalf("sidebar scrollTop = %d after a live replacement, want the member's 300", got)
	}
	if page.MustHas("#" + view.TopicDOMID(topics[49].ID)) {
		t.Fatal("the live replacement did not displace the selected topic")
	}
}
