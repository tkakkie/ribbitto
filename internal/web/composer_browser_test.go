package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func testComposerWithoutJavaScript(t *testing.T, browser *rod.Browser) {
	for _, topic := range []bool{false, true} {
		for _, older := range []bool{false, true} {
			t.Run(fmt.Sprintf("topic=%t/older=%t", topic, older), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				handler, _, _, place := composerHandler(t, topic)
				var signals atomic.Int32
				posts := make(chan url.Values, 2)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
					if strings.HasSuffix(r.URL.Path, "/typing") {
						signals.Add(1)
					}
					if r.Method == "POST" {
						if err := r.ParseForm(); err != nil {
							t.Error(err)
							return
						}
						if r.Header.Get("HX-Request") != "" {
							t.Error("JavaScript-disabled post was enhanced")
						}
					}
					handler.ServeHTTP(w, r)
					if r.Method == "POST" {
						posts <- r.PostForm
					}
				}))
				defer server.Close()
				path := view.ConversationURL("acme", place.Channel, nil)
				if topic {
					path = view.ConversationURL("acme", place.Channel, &place.Topic)
				}
				initial := path
				if older {
					initial += "?before=40"
				}
				page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
				defer page.MustClose()
				// Block the page's scripts (all are external files) rather than
				// disabling script execution, which rod itself needs for input.
				router := page.HijackRequests()
				router.MustAdd("*.js", func(h *rod.Hijack) { h.Response.Fail(proto.NetworkErrorReasonBlockedByClient) })
				go router.Run()
				defer router.MustStop()
				page.MustNavigate(server.URL + initial).MustWaitLoad()
				token := page.MustElement(`#message-composer input[name="composer"]`).MustAttribute("value")
				if token == nil || *token == "" {
					t.Fatal("missing browser generation")
				}
				if signal := page.MustElement(`#typing-form input[name="composer"]`).MustAttribute("value"); signal == nil || *signal != *token {
					t.Fatal("plain page's sibling form has a different generation")
				}
				page.MustElement("#message-body").MustInput("plain browser post")
				if signals.Load() != 0 {
					t.Fatal("input sent a typing request")
				}
				wait := page.WaitNavigation(proto.PageLifecycleEventNameLoad)
				page.MustElement(`#message-composer button[type="submit"]`).MustClick()
				wait()
				select {
				case values := <-posts:
					if values.Get("body") != "plain browser post" || len(values["composer"]) != 1 || values.Get("composer") != *token || values.Has("cursor") == older {
						t.Fatalf("wrong plain post controls: %v", values)
					}
				case <-ctx.Done():
					t.Fatal("plain form did not post", ctx.Err())
				}
				if page.MustInfo().URL != server.URL+path || page.MustElement("#message-body").MustText() != "" {
					t.Fatal("plain post did not redirect to cleared latest page")
				}
				fresh := page.MustElement(`#message-composer input[name="composer"]`).MustAttribute("value")
				if fresh == nil || *fresh == "" || *fresh == *token {
					t.Fatal("plain redirect retained old generation")
				}
				if signals.Load() != 0 || len(posts) != 0 {
					t.Fatal("unexpected typing or duplicate post")
				}
			})
		}
	}
}
