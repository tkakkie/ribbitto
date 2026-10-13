package web

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// Automatic reading posts through #read-form even while its control is
// hidden; without JavaScript the control is what reads an unread page.
func testReadControlBrowser(t *testing.T, browser *rod.Browser) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, script := range []bool{true, false} {
		for _, topic := range []bool{false, true} {
			t.Run(fmt.Sprintf("javascript=%t/topic=%t", script, topic), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				// With JavaScript nothing is unread; without it, something is.
				visit := &readControlVisit{fakeMessages: populatedMessages()}
				if !script {
					visit.feed, visit.topic = 7, 8
				}
				handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Messages, s.Reading = visit, visit }))
				if err != nil {
					t.Fatal(err)
				}
				var enhanced atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("HX-Request") != "" {
						enhanced.Add(1)
					}
					r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
					handler.ServeHTTP(w, r)
				}))
				defer server.Close()
				path := view.ChannelURL("acme", kernel.ID{1})
				if topic {
					path = view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{5})
				}
				page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
				defer page.MustClose()
				const shown = `() => { const f = document.querySelector('#read-form'), b = f?.querySelector('button[type=submit]');
					return {form: !!f, shown: !!b && b.getClientRects().length > 0, displayed: !!f && getComputedStyle(f).display !== 'none'}; }`
				if !script {
					router := page.HijackRequests()
					router.MustAdd("*.js", func(h *rod.Hijack) { h.Response.Fail(proto.NetworkErrorReasonBlockedByClient) })
					go router.Run()
					defer router.MustStop()
					page.MustNavigate(server.URL + path).MustWaitLoad()
					if state := page.MustEval(shown); !state.Get("shown").Bool() {
						t.Fatalf("no visible control with something unread: %s", state)
					}
					wait := page.WaitNavigation(proto.PageLifecycleEventNameLoad)
					page.MustElement(`#read-form button[type="submit"]`).MustClick()
					wait()
					if page.MustInfo().URL != server.URL+path || visit.reads.Load() != 1 || enhanced.Load() != 0 {
						t.Fatalf("plain read: url %s, reads %d, enhanced %d", page.MustInfo().URL, visit.reads.Load(), enhanced.Load())
					}
					if state := page.MustEval(shown); !state.Get("form").Bool() || state.Get("shown").Bool() || state.Get("displayed").Bool() {
						t.Fatalf("control still shown after reading: %s", state)
					}
					return
				}
				page.MustEvalOnNewDocument(`window.reads = []; document.addEventListener('htmx:afterRequest', e => {
					if (e.detail.elt?.id === 'read-form' && e.detail.successful) window.reads.push(e.detail.requestConfig.parameters.cursor);
				});`)
				page.MustNavigate(server.URL + path).MustWaitLoad()
				if state := page.MustEval(shown); !state.Get("form").Bool() || state.Get("shown").Bool() || state.Get("displayed").Bool() {
					t.Fatalf("control shown with nothing unread: %s", state)
				}
				// The visible load reads, and so does a later live application.
				page.MustWait(`() => window.reads.length === 1 && window.reads[0] === '42'`)
				page.MustEval(`() => {
					document.querySelector('#organization-stream').dataset.appliedCursor = '100';
					document.dispatchEvent(new Event('read-applied'));
				}`)
				page.MustWait(`() => window.reads.length === 2 && window.reads[1] === '100'`)
				if visit.reads.Load() != 2 {
					t.Fatalf("server saw %d reads, want 2", visit.reads.Load())
				}
			})
		}
	}
}
