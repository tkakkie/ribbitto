package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// Real PostgreSQL and real stream rendering, with delivery and POST answers
// independently gated at the browser boundary.
func TestPendingPostBrowser(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	browser := launchBrowser(t, ctx, "pending posts")
	for _, topic := range []bool{false, true} {
		t.Run(map[bool]string{false: "feed", true: "topic"}[topic], func(t *testing.T) { testPendingPostPage(t, ctx, browser, topic) })
	}
}

func testPendingPostPage(t *testing.T, ctx context.Context, browser *rod.Browser, topic bool) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "replacement", true: "lost-response"}[lost], func(t *testing.T) {
			pool, scope := feedFixture(t)
			handler, sessions, err := buildHandler(ctx, pool, handlerConfig{maxChannels: conversation.DefaultMaxChannels, hub: realtime.NewHub()})
			feedRequire(t, err)
			var account kernel.ID
			feedRequire(t, pool.QueryRow(ctx, "SELECT account_id FROM member WHERE organization_id=$1 AND id=$2", scope.OrganizationID, scope.MemberID).Scan(&account))
			token, _, err := sessions.Create(ctx, account)
			feedRequire(t, err)
			var reads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: token})
				if strings.HasSuffix(r.URL.Path, "/read") {
					reads.Add(1)
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			page, closePage := boundedPage(t, browser, ctx, "pending post")
			defer closePage()
			page.MustEvalOnNewDocument(`(() => { window.answers=0; window.holding=true;
   document.addEventListener('htmx:afterRequest',e=>{if(e.detail.requestConfig?.elt.matches('[data-pending-form]')) window.answers++;});
   document.addEventListener('htmx:sseBeforeMessage',e=>{if(window.holding && e.target.id==='message-items') {e.stopImmediatePropagation(); e.preventDefault(); window.delivery=e; }},true);
  })()`)
			path := view.ChannelURL("feed", scope.ChannelID)
			if topic {
				place := conversationtest.Topic(t, pool, scope.OrganizationID, scope.ChannelID, "planning")
				path = view.ConversationURL("feed", scope.ChannelID, &place.ID)
			}
			page.MustNavigate(server.URL + path).MustWaitLoad()
			page.MustWait(`() => !document.querySelector('#read-form.htmx-request')`)
			before := page.MustEval(`() => {const s=document.getElementById('organization-stream');return JSON.stringify([s.dataset.eventCursor,s.dataset.appliedCursor]);}`).Str()
			initialReads := reads.Load()
			sent, release := make(chan url.Values, 2), make(chan struct{}, 2)
			router := page.HijackRequests()
			var attempt atomic.Int32
			router.MustAdd(server.URL+path, func(h *rod.Hijack) {
				values, _ := url.ParseQuery(h.Request.Body())
				sent <- values
				n := attempt.Add(1)
				if err := h.LoadResponse(http.DefaultClient, true); err != nil {
					t.Error(err)
				}
				select {
				case <-release:
				case <-ctx.Done():
				}
				if lost && n == 1 {
					h.Response.Fail(proto.NetworkErrorReasonConnectionReset)
				}
			})
			go router.Run()
			defer func() { _ = router.Stop() }()
			page.MustEval(`() => {const b=document.getElementById('message-body'); window.cursor=document.getElementById('organization-stream').dataset.eventCursor; b.value='pending database post'; b.form.requestSubmit();}`)
			values := <-sent
			if values.Get("cursor") != page.MustEval(`() => window.cursor`).Str() {
				t.Fatal("wrong shown cursor")
			}
			assertPhase := func() {
				t.Helper()
				if page.MustEval(`before => {const s=document.getElementById('organization-stream');return JSON.stringify([s.dataset.eventCursor,s.dataset.appliedCursor])!==before || document.querySelectorAll('#message-pending > li').length!==1;}`, before).Bool() || reads.Load() != initialReads {
					t.Fatal("pending/response changed cursors, read, or disappeared")
				}
			}
			assertPhase()
			release <- struct{}{}
			page.MustWait(`() => window.answers===1`)
			assertPhase()
			if lost {
				page.MustElement("[data-pending-actions] button[type=submit]").MustClick()
				retry := <-sent
				if retry.Get("idempotency_key") != values.Get("idempotency_key") || retry.Get("body") != values.Get("body") {
					t.Fatal("retry changed text/key")
				}
				release <- struct{}{}
				page.MustWait(`() => window.answers===2`)
				assertPhase()
			}
			page.MustWait(`() => !!window.delivery`)
			page.MustEval(`() => { window.holding=false; const old=window.delivery; document.getElementById('message-items').dispatchEvent(new CustomEvent('htmx:sseBeforeMessage',{bubbles:true,cancelable:true,detail:old.detail})); }`)
			page.MustWait(`before => {const s=document.getElementById('organization-stream');return !document.querySelector('#message-pending > li') && !!s.dataset.appliedCursor && s.dataset.eventCursor===s.dataset.appliedCursor && JSON.stringify([s.dataset.eventCursor,s.dataset.appliedCursor])!==before && !document.querySelector('#read-form.htmx-request');}`, before)
			var count int
			var id kernel.ID
			feedRequire(t, pool.QueryRow(ctx, "SELECT count(*) FROM message WHERE organization_id=$1 AND channel_id=$2 AND body=$3", scope.OrganizationID, scope.ChannelID, "pending database post").Scan(&count))
			feedRequire(t, pool.QueryRow(ctx, "SELECT id FROM message WHERE organization_id=$1 AND channel_id=$2 AND body=$3", scope.OrganizationID, scope.ChannelID, "pending database post").Scan(&id))
			if page.MustElement("#body-"+view.MessageDOMID(id)).MustText() != "pending database post" {
				t.Fatal("stream body differs from pending text")
			}
			if count != 1 || reads.Load() != initialReads+1 || page.MustEval(`id => document.querySelectorAll('#message-items > #'+id).length!==1`, view.MessageDOMID(id)).Bool() {
				t.Fatal("delivery did not replace/read exactly once, or retry stored twice")
			}
		})
	}
}
