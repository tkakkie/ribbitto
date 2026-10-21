package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
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
		requestCtx, stop := context.WithTimeout(ctx, 8*time.Second)
		defer stop()
		select {
		case requests <- q:
		case <-requestCtx.Done():
			return
		}
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
		case <-requestCtx.Done(): // even an unexpected retry cannot hold server.Close
		}
	})
	mux.HandleFunc("GET /done", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("done")) })
	server := httptest.NewUnstartedServer(mux)
	// Chromium may retry a POST if a reused socket closes without response
	// bytes. Fresh connections make the intentional close an XHR failure.
	server.Config.SetKeepAlivesEnabled(false)
	server.Start()
	defer func() { cancel(); server.Close() }()
	t.Log("step: create page")
	type openedPage struct {
		page *rod.Page
		err  error
	}
	opened := make(chan openedPage, 1)
	go func() {
		var p *rod.Page
		err := rod.Try(func() { p = browser.Context(ctx).MustPage() })
		opened <- openedPage{p, err}
	}()
	var page *rod.Page
	select {
	case result := <-opened:
		if result.err != nil {
			t.Fatalf("step %q: %v", "create page", result.err)
		}
		page = result.page
	case <-time.After(15 * time.Second):
		t.Fatalf("step %q: page did not open within 15 s", "create page")
	}
	defer func() {
		cleanup := page.Timeout(time.Second)
		defer cleanup.CancelTimeout()
		_ = cleanup.Close()
	}()
	var lastRequest atomic.Value
	lastRequest.Store("none")
	step := func(name string, fn func(*rod.Page)) {
		t.Helper()
		t.Logf("step: %s", name)
		p := page.Timeout(8 * time.Second).WithPanic(func(v any) { panic(v) })
		defer p.CancelTimeout()
		// Rod's keyboard/mouse retain the original page context, so bound the
		// caller too; Try reports Must* panics with this step's diagnostics.
		done := make(chan error, 1)
		go func() { done <- rod.Try(func() { fn(p) }) }()
		var err error
		select {
		case err = <-done:
		case <-p.GetContext().Done():
			err = p.GetContext().Err()
		}
		if err != nil {
			diagnostic := page.Timeout(time.Second)
			defer diagnostic.CancelTimeout()
			state, stateErr := diagnostic.Eval(`() => JSON.stringify({answered: window.answered, uniqueAnswered: window.uniqueAnswered, settled: window.settled, request: window.lastRequest, focus: document.activeElement?.id})`)
			t.Fatalf("step %q: %v; last request: %s; window: %v (diagnostic error: %v)", name, err, lastRequest.Load(), state, stateErr)
		}
	}
	step("viewport", func(p *rod.Page) { p.MustSetViewport(width, 900, 1, width < 768) })
	step("navigate", func(p *rod.Page) {
		if err := navigateLoaded(p, server.URL); err != nil {
			panic(err)
		}
	})
	// Observe the real promise's outcomes, but return a never-settling promise
	// to production glue: retries must also work through lifecycle events.
	step("observe requests", func(p *rod.Page) {
		p.MustEval(`() => {
		  window.answered = 0; window.uniqueAnswered = 0; window.settled = [];
		  const ajax = htmx.ajax, answered = new WeakSet();
		  htmx.ajax = (...args) => { ajax(...args).then(() => window.settled.push('ok'), () => window.settled.push('error')); return new Promise(() => {}); };
		  document.addEventListener('submit', e => window.pinSubmit = e, true);
		  document.addEventListener('htmx:configRequest', e => {
		    const d = e.detail;
		    window.lastRequest = {source: d.elt.id, path: d.path, event: d.triggeringEvent?.type,
		      sameEvent: d.triggeringEvent === window.pinSubmit, form: d.triggeringEvent?.target.id};
		  });
		  document.addEventListener('htmx:afterRequest', e => {
		    window.answered++;
		    if (!answered.has(e.detail.xhr)) { answered.add(e.detail.xhr); window.uniqueAnswered++; }
		  });
		}`)
	})
	next := func(entry view.Message, pinned bool) (q request) {
		t.Helper()
		step("receive "+fmt.Sprintf("%x pinned=%t", entry.ID, pinned), func(p *rod.Page) {
			select {
			case q = <-requests:
			case <-p.GetContext().Done():
				panic("pin request did not arrive")
			}
			action := "/pin"
			if pinned {
				action = "/unpin"
			}
			lastRequest.Store(fmt.Sprintf("%+v", q))
			if q.path != entry.PinURL+action || q.hx != "true" || q.source != fmt.Sprintf("pin-%t-", pinned)+view.MessageDOMID(entry.ID) || q.target != "pin-form-"+view.MessageDOMID(entry.ID) {
				panic(fmt.Sprintf("wrong delegated request: %+v", q))
			}
			if !p.MustEval(`id => window.lastRequest.event === 'submit' && window.lastRequest.sameEvent && window.lastRequest.form === id`, q.target).Bool() {
				panic("requestConfig lost the original submit event")
			}
		})
		return q
	}
	noExtra := func() {
		t.Helper()
		step("no duplicate request", func(p *rod.Page) {
			select {
			case q := <-requests:
				panic("duplicate pin request: " + q.path)
			case <-time.After(noReadWindow):
			}
		})
	}
	activate := func(entry view.Message, pinned bool, key input.Key) {
		t.Helper()
		name := fmt.Sprintf("%x pinned=%t key=%v", entry.ID, pinned, key)
		step("open menu "+name, func(p *rod.Page) {
			p.MustEval(`id => { const m = document.getElementById(id); if (!m.matches(':popover-open')) m.showPopover(); }`, fmt.Sprintf("message-actions-%x", entry.ID))
		})
		button := fmt.Sprintf("#pin-%t-", pinned) + view.MessageDOMID(entry.ID)
		for attempt := range 2 {
			step(fmt.Sprintf("activate %s attempt=%d", name, attempt+1), func(p *rod.Page) {
				if key == 0 {
					p.MustElement(button).MustClick()
				} else {
					p.MustElement(button).MustFocus()
					p.Keyboard.MustType(key)
				}
			})
		}
	}
	completed := 0
	wait := func(outcome string) {
		t.Helper()
		completed++
		step(fmt.Sprintf("complete request %d (%s)", completed, outcome), func(p *rod.Page) {
			// afterRequest can bubble from the source and then a surviving
			// ancestor after outerHTML. Count XHRs and promise outcomes instead.
			p.MustWait(`(n, outcome) => window.uniqueAnswered >= n && window.settled.length >= n && window.settled[n-1] === outcome`, completed, outcome)
		})
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
			step("swapped state and button focus", func(p *rod.Page) {
				p.MustWait(`state => document.activeElement.dataset.pinAction === state && document.activeElement.form.dataset.pinned === state`, fmt.Sprint(!pinned))
			})
			noExtra() // duplicates must be dropped, never queued after completion
		}
	}
	// 404 represents the permission rejection covered by TestPinRoutes.
	// HTTP errors settle successfully in 2.0.7; only network errors reject.
	for _, status := range []int{409, 404, 503, 0, 200} {
		activate(a, false, input.Enter)
		q := next(a, false)
		noExtra()
		step(fmt.Sprintf("remember control before status %d", status), func(p *rod.Page) {
			p.MustEval(`() => window.beforePin = document.querySelector('[data-pin-control]')`)
		})
		q.reply <- browserReply{status, []byte(renderIn(t, "en", view.PinControl(a, "pins.error.limit")))}
		outcome := "ok"
		if status == 0 {
			outcome = "error"
		}
		wait(outcome)
		step(fmt.Sprintf("status %d state, alert and swap policy", status), func(p *rod.Page) {
			if !p.MustEval(`status => {
			  const f = document.querySelector('[data-pin-control]');
			  return f.dataset.pinned === 'false' && !f.querySelector('[data-pin-action=false]').hidden &&
			    (status === 409 ? !!f.querySelector('[role=alert]')?.checkVisibility() : true) &&
			    ([404,503,0].includes(status) ? f === window.beforePin : f !== window.beforePin);
			}`, status).Bool() {
				panic("changed state or lost error/no-swap behaviour")
			}
		})
		noExtra()
	}
	// Simulate onload failures without afterRequest or a settled promise.
	// Cover detail.elt and the surviving-ancestor/detached-source lookup.
	for _, ancestor := range []bool{false, true} {
		step(fmt.Sprintf("onLoadError ancestor=%t", ancestor), func(p *rod.Page) {
			p.MustEval(`(id, ancestor) => {
			  const ajax = htmx.ajax;
			  htmx.ajax = (method, url, options) => {
			    htmx.ajax = ajax;
			    const source = options.source, elt = ancestor ? document.body : source;
			    const form = source.closest("[data-pin-control]"), parent = form.parentElement, next = form.nextSibling;
			    if (ancestor) form.remove();
			    elt.dispatchEvent(new CustomEvent('htmx:onLoadError', {bubbles: true,
			      detail: ancestor ? {elt, requestConfig: {elt: source}} : {elt}}));
			    if (ancestor) parent.insertBefore(form, next);
			    return new Promise(() => {});
			  };
			  document.getElementById(id).requestSubmit();
			}`, "pin-form-"+view.MessageDOMID(a.ID), ancestor)
		})
		noExtra()
		step("requestSubmit without submitter retries visible action", func(p *rod.Page) {
			p.MustEval(`id => document.getElementById(id).requestSubmit()`, "pin-form-"+view.MessageDOMID(a.ID))
		})
		q := next(a, ancestor) // first Pin, then Unpin: choose the non-hidden action
		changed := a
		changed.Pinned = !ancestor
		q.reply <- browserReply{200, []byte(renderIn(t, "en", view.PinControl(changed, "")))}
		wait("ok")
		noExtra()
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
	step("independent state and focus", func(p *rod.Page) {
		if !p.MustEval(`() => [...document.querySelectorAll('[data-pin-control]')].every(f => f.dataset.pinned === 'true') && document.activeElement === document.querySelector('[data-pin-action=true]')`).Bool() {
			panic("independent requests changed another control or stole focus")
		}
	})
	noExtra()
	step("native fallback without htmx", func(p *rod.Page) {
		p.MustEval(`() => { window.htmx = undefined; }`)
		p.MustElement("#pin-true-" + view.MessageDOMID(a.ID)).MustClick()
	})
	step("native POST and 303 redirect", func(p *rod.Page) {
		select {
		case q := <-requests:
			if q.hx != "" || q.path != a.PinURL+"/unpin" {
				panic(fmt.Sprintf("missing-htmx fallback: %+v", q))
			}
			q.reply <- browserReply{status: 303}
		case <-p.GetContext().Done():
			panic("native fallback did not POST")
		}
		p.MustWait(`() => location.pathname === '/done'`)
	})
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
