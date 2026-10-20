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
	"github.com/go-rod/rod/lib/input"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

func testPinsBrowser(t *testing.T, browser *rod.Browser) {
	for _, width := range []int{1440, 390} {
		t.Run(fmt.Sprintf("submit/width=%d", width), func(t *testing.T) { testPinSubmitBrowser(t, browser, width) })
	}
	t.Run("frame", func(t *testing.T) {
		testPinFrameBrowser(t, browser)
	})
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprintf("refused=%t", refused), func(t *testing.T) {
			testPinRaceBrowser(t, browser, refused)
		})
	}
}

func testPinSubmitBrowser(t *testing.T, browser *rod.Browser, width int) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	snapshot := feedFixture(t, 2)
	snapshot.Before = 20 // no automatic read POSTs
	type request struct {
		path, hx, source, target string
		reply                    chan browserReply
	}
	requests := make(chan request, 8)
	mux := messagePageMux(t, snapshot, http.FileServerFS(static.FS()))
	mux.HandleFunc("POST /organizations/acme/channels/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		q := request{r.URL.Path, r.Header.Get("HX-Request"), r.Header.Get("HX-Trigger"), r.Header.Get("HX-Target"), make(chan browserReply, 1)}
		requests <- q
		select {
		case reply := <-q.reply:
			if reply.status == 0 {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close() // real network error, not an HTTP error response
				return
			}
			if reply.status == 303 {
				w.Header().Set("Location", "/done")
			}
			w.WriteHeader(reply.status)
			_, _ = w.Write(reply.body)
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("GET /done", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("done")) })
	server := httptest.NewServer(mux)
	defer server.Close()
	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	defer page.MustClose()
	page.MustSetViewport(width, 900, 1, width < 768)
	if err := navigateLoaded(page, server.URL); err != nil {
		t.Fatal(err)
	}
	// Observe the real promise's outcomes, but return a never-settling promise
	// to production glue: retries must also work through lifecycle events.
	page.MustEval(`() => {
	  window.answered = 0; window.settled = [];
	  const ajax = htmx.ajax;
	  htmx.ajax = (...args) => { ajax(...args).then(() => window.settled.push('ok'), () => window.settled.push('error')); return new Promise(() => {}); };
	  document.addEventListener('htmx:afterRequest', () => window.answered++);
	}`)
	next := func(entry view.Message, pinned bool) request {
		t.Helper()
		select {
		case q := <-requests:
			action := "/pin"
			if pinned {
				action = "/unpin"
			}
			if q.path != entry.PinURL+action || q.hx != "true" || q.source != fmt.Sprintf("pin-%t-", pinned)+view.MessageDOMID(entry.ID) || q.target != "pin-form-"+view.MessageDOMID(entry.ID) {
				t.Fatalf("wrong delegated request: %+v", q)
			}
			return q
		case <-ctx.Done():
			t.Fatal("pin request did not arrive")
			return request{}
		}
	}
	noExtra := func() {
		t.Helper()
		select {
		case q := <-requests:
			t.Fatalf("duplicate pin request: %s", q.path)
		case <-time.After(noReadWindow):
		}
	}
	activate := func(entry view.Message, pinned bool, key input.Key) {
		t.Helper()
		page.MustEval(`id => { const m = document.getElementById(id); if (!m.matches(':popover-open')) m.showPopover(); }`, fmt.Sprintf("message-actions-%x", entry.ID))
		button := page.MustElement(fmt.Sprintf("#pin-%t-", pinned) + view.MessageDOMID(entry.ID))
		if key == 0 {
			button.MustClick().MustClick()
		} else {
			button.MustFocus()
			page.Keyboard.MustType(key, key)
		}
	}
	answered := 0
	wait := func(outcome string) {
		t.Helper()
		answered++
		page.MustWait(`(n, outcome) => window.answered === n && window.settled.length === n && window.settled[n-1] === outcome`, answered, outcome)
	}
	a, b := snapshot.Messages[0], snapshot.Messages[1]
	for _, key := range []input.Key{0, input.Enter, input.Space} {
		for _, pinned := range []bool{false, true} {
			activate(a, pinned, key)
			q := next(a, pinned)
			noExtra()
			changed := a
			changed.Pinned = !pinned
			q.reply <- browserReply{200, []byte(renderIn(t, "en", view.PinControl(changed, "")))}
			wait("ok")
			page.MustWait(`state => document.activeElement.dataset.pinAction === state && document.activeElement.form.dataset.pinned === state`, fmt.Sprint(!pinned))
			noExtra() // duplicates must be dropped, never queued after completion
		}
	}
	// 404 represents the permission rejection covered by TestPinRoutes.
	// HTTP errors settle successfully in 2.0.7; only network errors reject.
	for _, status := range []int{409, 404, 503, 0, 200} {
		activate(a, false, input.Enter)
		q := next(a, false)
		page.MustEval(`() => window.beforePin = document.querySelector('[data-pin-control]')`)
		q.reply <- browserReply{status, []byte(renderIn(t, "en", view.PinControl(a, "pins.error.limit")))}
		outcome := "ok"
		if status == 0 {
			outcome = "error"
		}
		wait(outcome)
		if !page.MustEval(`status => {
		  const f = document.querySelector('[data-pin-control]');
		  return f.dataset.pinned === 'false' && !f.querySelector('[data-pin-action=false]').hidden &&
		    (status === 409 ? !!f.querySelector('[role=alert]')?.checkVisibility() : true) &&
		    ([404,503,0].includes(status) ? f === window.beforePin : f !== window.beforePin);
		}`, status).Bool() {
			t.Fatalf("status %d changed state or lost error/no-swap behaviour", status)
		}
	}
	// B remains pending while A completes; B's later response cannot touch A.
	activate(b, false, input.Enter)
	qb := next(b, false)
	activate(a, false, 0)
	qa := next(a, false)
	a.Pinned, b.Pinned = true, true
	qa.reply <- browserReply{200, []byte(renderIn(t, "en", view.PinControl(a, "")))}
	wait("ok")
	qb.reply <- browserReply{200, []byte(renderIn(t, "en", view.PinControl(b, "")))}
	wait("ok")
	if !page.MustEval(`() => [...document.querySelectorAll('[data-pin-control]')].every(f => f.dataset.pinned === 'true') && document.activeElement === document.querySelector('[data-pin-action=true]')`).Bool() {
		t.Fatal("independent requests changed another control or stole focus")
	}
	noExtra()
	page.MustEval(`() => { window.htmx = undefined; }`)
	page.MustElement("#pin-true-" + view.MessageDOMID(a.ID)).MustClick()
	select {
	case q := <-requests:
		if q.hx != "" || q.path != a.PinURL+"/unpin" {
			t.Fatalf("missing-htmx fallback: %+v", q)
		}
		q.reply <- browserReply{status: 303}
	case <-ctx.Done():
		t.Fatal("native fallback did not POST")
	}
	page.MustWait(`() => location.pathname === '/done'`)
}

func testPinFrameBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	snapshot := seedMessagePage(t)
	snapshot.Panel, snapshot.Before, snapshot.SnapshotSeq = "overview", 20, 42
	entry := snapshot.Messages[0]
	entry.PinURL = view.PinURL("acme", snapshot.Current.ID, entry.ID)
	snapshot.Messages[0] = entry
	pinned := snapshot
	pinned.SnapshotSeq, pinned.Pins = 43, []view.Pin{{MessageID: entry.ID, Excerpt: "pinned"}}
	frame := renderIn(t, "en", view.PinsSection(pinned))
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.Handle("/", messagePageMux(t, snapshot, http.FileServerFS(static.FS())))
	mux.HandleFunc("GET /organizations/acme/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
		if err := sender.Heartbeat(r.Context()); err != nil {
			return
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		if err := sender.Send(r.Context(), realtime.Outgoing{Name: "pins", Data: []byte(frame), Ephemeral: true}); err != nil {
			return
		}
		<-r.Context().Done()
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	defer page.MustClose()
	page.MustSetViewport(1440, 900, 1, false)
	if err := navigateLoaded(page, server.URL); err != nil {
		t.Fatal(err)
	}
	page.MustEval(`() => {
  window.pinRequests = 0;
  document.addEventListener('htmx:beforeRequest', () => window.pinRequests++);
  document.addEventListener('htmx:afterSwap', e => { if (e.detail.elt?.id === 'pins-section') window.pinFrameSwapped = true; });
 }`)
	page.MustElement("[popovertarget='message-actions-" + fmt.Sprintf("%x", entry.ID) + "']").MustClick()
	if !page.MustEval(`() => document.querySelector('[data-pin-control]').dataset.pinned === 'false' && document.querySelector('[data-pin-action=false]').innerText.trim() === 'Pin' && document.querySelector('[data-pin-action=false]').getClientRects().length > 0 && document.querySelector('[data-pin-action=true]').hidden`).Bool() {
		t.Fatal("loaded control does not show Pin")
	}
	// Only the stream delivers the change; this page makes no htmx request.
	close(release)
	page.MustWait(`() => window.pinFrameSwapped === true`)
	if !page.MustEval(`() => window.pinRequests === 0 && document.getElementById('pins-section').dataset.pinsSeq === '43' && document.querySelector('[data-pin-control]').dataset.pinned === 'true' && document.querySelector('[data-pin-action=true]').innerText.trim() === 'Unpin' && document.querySelector('[data-pin-action=true]').getClientRects().length > 0 && document.querySelector('[data-pin-action=false]').hidden`).Bool() {
		t.Fatal("pins frame did not switch the loaded control to Unpin without an htmx request")
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
