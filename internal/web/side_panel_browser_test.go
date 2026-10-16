package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

// The members panel keeps the conversation live on the page's one stream;
// Escape closes it with a page load and returns focus to its control (#874).
func testSidePanelBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cursor, topic := int64(42), kernel.ID{2}
	conversationPath := view.ChannelURL("acme", kernel.ID{1})
	streams := make(chan url.Values, 2)
	frames := make(chan realtime.Outgoing)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/organizations/acme/events" {
			page := view.ChannelPage{Organization: view.Organization{Name: "Acme", Slug: "acme"},
				Current: view.Channel{ID: kernel.ID{1}, Name: "general"}, EventCursor: &cursor}
			if strings.HasSuffix(r.URL.Path, "/members") {
				page.Panel = view.PanelMembers
				page.Members = &view.MemberPage{PresenceAfter: "process:7", Items: []view.Member{{ID: kernel.ID{3}, Handle: "alice"}}}
			}
			templ.Handler(view.ChannelScreen("/static/css/app.css", page)).ServeHTTP(w, r)
			return
		}
		select {
		case streams <- r.URL.Query():
		case <-r.Context().Done():
			return
		}
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
	stream := func(want, presence string) {
		t.Helper()
		select {
		case q := <-streams:
			if q.Get("want") != want || q.Get("presence-after") != presence || q.Get("after") != "42" || q.Has("panel") {
				t.Fatalf("stream interests = %v, want %s with presence-after %q", q, want, presence)
			}
		case <-ctx.Done():
			t.Fatal("stream did not connect", ctx.Err())
		}
	}
	send := func(out realtime.Outgoing) {
		t.Helper()
		select {
		case frames <- out:
		case <-ctx.Done():
			t.Fatal("stream did not take the frame", ctx.Err())
		}
	}
	page.MustNavigate(server.URL + conversationPath + "/members").MustWaitLoad()
	stream("sidebar,messages,typing,presence", "process:7")
	page.MustWait(`() => document.activeElement.id === 'panel-heading'`)
	send(realtime.Outgoing{ID: 43, Name: "message", Data: streamTestMarkup(t, view.LiveMessageItem(streamTestMessages(topic, 43)[0]))})
	page.MustWait(`() => document.getElementById('organization-stream').dataset.eventCursor === '43'`)
	send(realtime.Outgoing{Ephemeral: true, Name: "typing", Data: streamTestMarkup(t, view.TypingIndicator(typing.Snapshot{Typists: []typing.Typist{{Handle: "alice"}}}))})
	page.MustWait(`() => document.getElementById('typing-status').textContent.includes('alice')`)
	assertBrowserItems(t, page, []int{43})
	// Escape in the composer leaves the panel and the draft alone.
	page.MustElement("#message-body").MustInput("draft")
	page.Keyboard.MustType(input.Escape)
	if !page.MustEval(`() => sessionStorage.getItem('panel-opener') === null && document.getElementById('message-body').value.endsWith('draft')`).Bool() {
		t.Fatal("Escape in the composer closed the panel")
	}
	page.MustElement("#panel-heading").MustFocus()
	page.Keyboard.MustType(input.Escape)
	// The wait polls across the closing page load: until the parser reaches
	// <body>, which arrival-v1.js in <head> holds back, the new document has
	// no active element at all (#1251).
	page.MustWait(`() => !document.getElementById('side-panel') && document.activeElement?.id === 'panel-control-members'`)
	if got := page.MustInfo().URL; got != server.URL+conversationPath {
		t.Fatalf("closed to %s", got)
	}
	stream("sidebar,messages,typing", "")
}
