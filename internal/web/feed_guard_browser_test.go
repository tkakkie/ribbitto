package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

func testFeedGuardHTMXLoad(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	fixture := feedFixture(t, 50)
	markup := renderIn(t, "en", view.ChannelScreen("/static/css/app.css", fixture))
	want := measureFeed(t, markup).values[4]
	next := feedFixture(t, 51).Messages[50]
	frame := realtime.Outgoing{ID: 51, Name: "message", Data: []byte(renderIn(t, "en", view.LiveMessageItem(next)))}
	gate := make(chan struct{})
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("POST /organizations/acme/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /organizations/acme/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
		if sender.Heartbeat(r.Context()) != nil {
			return
		}
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		if sender.Send(r.Context(), frame) != nil {
			return
		}
		<-r.Context().Done()
	})
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, markup)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	tab, closeTab := boundedPage(t, browser, ctx, "htmx load count")
	defer closeTab()
	step := pageStep(ctx, tab)
	step(t, "installing document-start htmx counters", func(p *rod.Page) {
		p.MustEvalOnNewDocument(`(() => {
   window.totalCount = 0; window.bodyLoadCount = 0;
   document.addEventListener('htmx:beforeProcessNode', () => { window.totalCount++; }, true);
   document.addEventListener('htmx:load', e => {
    if (e.target === document.body) {
     window.bodyLoadCount++;
     if (window.loadCount === undefined) window.loadCount = window.totalCount;
    }
   }, true);
  })()`)
	})
	step(t, "navigating to the 50-message page", func(p *rod.Page) { p.MustNavigate(server.URL).MustWaitLoad() })
	loadCount := 0
	step(t, "waiting for initial body htmx:load to freeze loadCount and comparing it", func(p *rod.Page) {
		p.MustWait(`() => window.loadCount !== undefined`)
		loadCount = p.MustEval(`() => window.loadCount`).Int()
		if freezes := p.MustEval(`() => window.bodyLoadCount`).Int(); freezes != 1 {
			t.Fatalf("initial load frozen %d times, want exactly once", freezes)
		}
		if loadCount != want {
			t.Fatalf("initial htmx processed nodes: browser %d, Go mirror %d", loadCount, want)
		}
		t.Logf("initial htmx processed nodes: browser=%d mirror=%d", loadCount, want)
	})
	step(t, "releasing one stream message and waiting for its DOM item", func(p *rod.Page) {
		close(gate)
		p.MustWait(`() => !!document.querySelector('#message-items > li[data-event-seq="51"]')`)
	})
	step(t, "proving a count including the stream frame differs from initial load", func(p *rod.Page) {
		total := p.MustEval(`() => window.totalCount`).Int()
		if total == loadCount || total < loadCount {
			t.Fatalf("stream-inclusive negative case: totalCount=%d must differ from and exceed loadCount=%d", total, loadCount)
		}
		if freezes := p.MustEval(`() => window.bodyLoadCount`).Int(); freezes != 1 {
			t.Fatalf("initial load frozen %d times after stream frame, want exactly once", freezes)
		}
		t.Logf("stream-inclusive count=%d, frozen load count=%d", total, loadCount)
	})
}
