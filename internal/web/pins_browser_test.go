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
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

func testPinsBrowser(t *testing.T, browser *rod.Browser) {
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprintf("refused=%t", refused), func(t *testing.T) {
			testPinRaceBrowser(t, browser, refused)
		})
	}
}

func testPinRaceBrowser(t *testing.T, browser *rod.Browser, refused bool) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	snapshot := seedMessagePage(t)
	snapshot.Panel, snapshot.Before, snapshot.SnapshotSeq = "overview", 20, 42
	entry := snapshot.Messages[0]
	entry.PinURL = view.PinURL("acme", snapshot.Current.ID, entry.ID)
	snapshot.Messages[0] = entry
	pinned := snapshot
	pinned.SnapshotSeq, pinned.Pins = 43, []view.Pin{{MessageID: entry.ID, Excerpt: "pinned"}}
	old := renderIn(t, "en", view.PinsSection(snapshot))
	current := renderIn(t, "en", view.PinsSection(pinned))
	release := make(chan struct{}, 1)
	marker := make(chan string, 1)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("/organizations/acme/events", idleStream)
	mux.HandleFunc(entry.PinURL+"/pin", func(w http.ResponseWriter, r *http.Request) {
		marker <- r.Header.Get("X-Pins-Section")
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		pinnedEntry := entry
		pinnedEntry.Pinned = true
		if refused {
			templ.Handler(view.PinResponse(entry, "pins.error.limit", snapshot, true), templ.WithStatus(http.StatusConflict)).ServeHTTP(w, r)
			return
		}
		templ.Handler(view.PinResponse(pinnedEntry, "", pinned, true)).ServeHTTP(w, r)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		templ.Handler(view.ChannelScreen("/static/css/app.css", snapshot)).ServeHTTP(w, r)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	defer page.MustClose()
	page.MustSetViewport(1440, 900, 1, false)
	if err := navigateLoaded(page, server.URL); err != nil {
		t.Fatal(err)
	}
	page.MustEval(`() => { document.addEventListener('htmx:afterRequest', () => { window.pinAnswered = true; }); document.getElementById('message-body').value = 'keep draft'; window.range = Array.from(document.querySelectorAll('#message-items > li'), e => e.id + ':' + e.dataset.eventSeq).join(); }`)
	button := "#pin-false-" + view.MessageDOMID(entry.ID)
	page.MustElement("[popovertarget='message-actions-" + fmt.Sprintf("%x", entry.ID) + "']").MustClick()
	page.MustElement(button).MustClick()
	// The newer frame arrives before the response. Then an old frame follows it.
	deliver := func(html string) {
		page.MustEval(`(html) => {
  const target = document.getElementById('pins-section');
  if (htmx.trigger(target, 'htmx:sseBeforeMessage', {type:'pins', data:html})) htmx.swap(target, html, {swapStyle:'outerHTML', settleDelay:0});
 }`, html)
	}
	deliver(current)
	release <- struct{}{}
	page.MustWait(`() => window.pinAnswered === true`)
	page.MustWait(`() => !document.querySelector('[data-pin-control]').classList.contains('htmx-settling')`)
	if got := page.MustEval(`() => { const alert = document.querySelector('[data-pin-control] [role=alert]'); return !!alert && alert.getClientRects().length > 0; }`).Bool(); got != refused {
		t.Fatalf("refused=%t: alert visible=%t", refused, got)
	}
	deliver(old)
	if !page.MustEval(`() => document.getElementById('pins-section').dataset.pinsSeq === '43' && document.getElementById('message-body').value === 'keep draft' && Array.from(document.querySelectorAll('#message-items > li'), e => e.id + ':' + e.dataset.eventSeq).join() === window.range && document.activeElement.dataset.pinAction === 'true' && document.querySelector('[data-pin-control]').dataset.pinned === 'true' && !document.querySelector('[data-pin-action=true]').hidden && document.querySelector('[data-pin-action=false]').hidden`).Bool() {
		t.Fatal("pins lost snapshot or draft")
	}
	if got := <-marker; got != "true" {
		t.Fatalf("overview request marker = %q, want true", got)
	}
}
