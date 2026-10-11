package web

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

type readBrowserReply struct {
	status int
	after  string
}

type readBrowserRequest struct {
	page, cursor string
	reply        chan readBrowserReply
}

// Only the read script's timers use the fake clock; htmx and stream lifecycle
// timers still run normally. Gates hold real HTTP responses across all pages.
const readBrowserClock = `(() => {
 const timers = new Map(); let next = 0;
 window.readTest = {completed: 0, started: 0, active: 0, maxActive: 0, maxTimers: 0, random: 0};
 const test = window.readTest, original = window.setTimeout, cancel = window.clearTimeout;
 Object.defineProperty(document, 'visibilityState', {configurable: true, get: () => test.hidden ? 'hidden' : 'visible'});
 Math.random = () => test.random;
 Date.now = () => 1700000000000;
 window.setTimeout = (fn, ms, ...args) => {
  if (!new Error().stack.includes('read-visibility-v4.js')) return original(fn, ms, ...args);
  const id = --next; timers.set(id, {fn: () => fn(...args), ms});
  test.maxTimers = Math.max(test.maxTimers, timers.size); return id;
 };
 window.clearTimeout = id => { if (!timers.delete(id)) cancel(id); };
 test.delays = () => [...timers.values()].map(t => t.ms);
 test.advance = () => { const pending = [...timers.values()]; timers.clear(); pending.forEach(t => t.fn()); };
 test.apply = cursor => {
  document.querySelector('#organization-stream').dataset.appliedCursor = cursor;
  document.dispatchEvent(new Event('read-applied'));
 };
 document.addEventListener('htmx:configRequest', e => {
  if (e.detail.elt.id === 'read-form') e.detail.headers['X-Test-Page'] = test.id;
 });
 document.addEventListener('htmx:beforeSend', e => {
  if (e.detail.elt.id !== 'read-form') return;
  test.started++; test.active++; test.maxActive = Math.max(test.maxActive, test.active);
 });
 document.addEventListener('htmx:afterRequest', e => {
  if (e.detail.elt.id !== 'read-form') return;
  test.completed++; test.active--;
 });
})()`

func testReadRecoveryBrowser(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	visit := &dividerVisit{}
	handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Messages, s.Reading = visit, visit }))
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan readBrowserRequest, 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/read") {
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			handler.ServeHTTP(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		request := readBrowserRequest{r.Header.Get("X-Test-Page"), r.Form.Get("cursor"), make(chan readBrowserReply, 1)}
		select {
		case requests <- request:
		case <-ctx.Done():
			return
		case <-r.Context().Done():
			return
		}
		select {
		case reply := <-request.reply:
			if reply.after != "" {
				w.Header().Set("Retry-After", reply.after)
			}
			w.WriteHeader(reply.status)
		case <-ctx.Done():
		case <-r.Context().Done():
		}
	}))
	defer func() { cancel(); server.Close() }()
	next := func(t *testing.T) readBrowserRequest {
		t.Helper()
		select {
		case request := <-requests:
			return request
		case <-ctx.Done():
			t.Fatal("read request did not arrive")
			return readBrowserRequest{}
		}
	}
	pages := make(map[string]*rod.Page)
	open := func(t *testing.T, id string, topic bool) *rod.Page {
		t.Helper()
		page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
		pages[id] = page
		// Close pages before the server waits for any outstanding request gates.
		page.MustEvalOnNewDocument(readBrowserClock + fmt.Sprintf(";window.readTest.id = %q;", id))
		path := view.ChannelURL("acme", kernel.ID{1})
		if topic {
			path = view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{5})
		}
		page.MustNavigate(server.URL + path).MustWaitLoad()
		return page
	}
	defer func() {
		for _, page := range pages {
			_ = page.Context(context.WithoutCancel(ctx)).Timeout(5 * time.Second).Close()
		}
	}()
	finish := func(t *testing.T, request readBrowserRequest, status int, after string, completed int) {
		t.Helper()
		request.reply <- readBrowserReply{status, after}
		pages[request.page].MustWait(`n => readTest.completed === n`, completed)
		// afterRequest queues the flush after htmx releases its request lock.
		pages[request.page].MustEval(`() => new Promise(resolve => queueMicrotask(resolve))`)
	}
	assertState := func(t *testing.T, page *rod.Page, timers, started int) {
		t.Helper()
		if !page.MustEval(`(timers, started) => readTest.delays().length === timers && readTest.started === started && readTest.maxTimers <= 1 && readTest.maxActive <= 1`, timers, started).Bool() {
			t.Fatalf("one timer/request or request count violated: %s", page.MustEval(`() => JSON.stringify({...readTest, delays: readTest.delays()})`).Str())
		}
	}
	assertDelay := func(t *testing.T, page *rod.Page, low, high float64) {
		t.Helper()
		if !page.MustEval(`(low, high) => readTest.delays().length === 1 && readTest.delays()[0] >= low && readTest.delays()[0] <= high`, low, high).Bool() {
			t.Fatalf("retry delay outside [%g,%g]: %s", low, high, page.MustEval(`() => JSON.stringify(readTest.delays())`).Str())
		}
	}
	t.Run("sixteen-pages", func(t *testing.T) {
		pending := make([]readBrowserRequest, 16)
		for i := range pending {
			open(t, fmt.Sprint(i), i%2 == 1)
			pending[i] = next(t)
		}
		// All sixteen reads overlap, with repeated refusals and no user trigger.
		for round := 1; round <= 3; round++ {
			for _, request := range pending {
				page := pages[request.page]
				assertState(t, page, 0, round)
				finish(t, request, 429, "1", round)
				assertState(t, page, 1, round)
				assertDelay(t, page, 1000, 8000)
			}
			for _, request := range pending {
				pages[request.page].MustEval(`() => readTest.advance()`)
			}
			for i := range pending {
				pending[i] = next(t)
			}
		}
		for _, request := range pending {
			finish(t, request, 204, "", 4)
			assertState(t, pages[request.page], 0, 4)
		}
	})
	t.Run("coalescing-backoff-reset", func(t *testing.T) {
		page := open(t, "coalesce", false)
		request := next(t)
		page.MustEval(`() => { readTest.apply('100'); readTest.apply('101'); }`)
		assertState(t, page, 0, 1)
		for round := 1; round <= 6; round++ {
			after, low, high := "", float64(500), float64(1000)
			if round > 1 {
				low = float64(int(1000) << min(round-2, 2))
				high = low * 2
			}
			if round == 1 {
				after, low, high = "3", 3000, 3000
			}
			if round == 2 {
				after, low, high = "Tue, 14 Nov 2023 22:13:30 GMT", 10000, 10000
			}
			if round == 3 {
				after = "invalid"
			}
			page.MustEval(`r => { readTest.random = r; }`, float64(round%2)*0.999)
			finish(t, request, 429, after, round)
			assertDelay(t, page, low, high)
			if after == "" || after == "invalid" {
				delay := high * (0.5 + float64(round%2)*0.999/2)
				assertDelay(t, page, delay-0.01, delay+0.01)
			}
			page.MustEval(`() => { readTest.apply('102'); readTest.apply('103'); }`)
			assertState(t, page, 1, round)
			page.MustEval(`() => readTest.advance()`)
			request = next(t)
			if request.cursor != "103" {
				t.Fatalf("retry cursor=%s, want latest applied 103", request.cursor)
			}
		}
		finish(t, request, 204, "", 7)
		page.MustEval(`() => readTest.apply('103')`)
		assertState(t, page, 0, 7)
		page.MustEval(`() => readTest.apply('104')`)
		request = next(t)
		finish(t, request, 429, "", 8)
		assertDelay(t, page, 500, 1000)
		page.MustEval(`() => readTest.advance()`)
		finish(t, next(t), 204, "", 9)
		assertState(t, page, 0, 9)
	})
	t.Run("replacement", func(t *testing.T) {
		page := open(t, "replacement", false)
		request := next(t)
		page.MustEval(`() => {
            const old = document.querySelector('#read-form'), form = old.cloneNode(true);
            form.elements.cursor.value = '210'; old.replaceWith(form);
            htmx.process(form); htmx.trigger(form, 'htmx:load', {elt: form});
        }`)
		assertState(t, page, 0, 1)
		finish(t, request, 429, "1", 1)
		request = next(t)
		if request.cursor != "210" {
			t.Fatalf("replacement cursor=%s, want 210", request.cursor)
		}
		finish(t, request, 204, "", 2)
		assertState(t, page, 0, 2)
	})
	t.Run("cleanup", func(t *testing.T) {
		for _, action := range []string{"hide", "remove", "navigation"} {
			page := open(t, action, false)
			finish(t, next(t), 429, "1", 1)
			assertState(t, page, 1, 1)
			page.MustEval(`action => {
			 if (action === 'hide') { readTest.hidden = true; document.dispatchEvent(new Event('visibilitychange')); }
			 if (action === 'remove') document.querySelector('#read-form').remove();
			 if (action === 'navigation') window.dispatchEvent(new PageTransitionEvent('pagehide'));
			}`, action)
			page.MustEval(`() => new Promise(resolve => queueMicrotask(resolve))`)
			assertState(t, page, 0, 1)
			page.MustEval(`() => { readTest.advance(); document.dispatchEvent(new Event('read-applied')); }`)
			assertState(t, page, 0, 1)
		}
	})
	t.Run("non-429", func(t *testing.T) {
		for _, status := range []int{400, 500} {
			page := open(t, fmt.Sprint(status), false)
			finish(t, next(t), status, "1", 1)
			assertState(t, page, 0, 1)
			page.MustEval(`() => readTest.apply('200')`)
			request := next(t)
			page.MustEval(`() => readTest.apply('201')`)
			finish(t, request, status, "", 2)
			request = next(t)
			if request.cursor != "201" {
				t.Fatalf("non-429 flush cursor=%s, want 201", request.cursor)
			}
			finish(t, request, 204, "", 3)
			assertState(t, page, 0, 3)
		}
	})
}
