package main

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
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// Real PostgreSQL and real stream rendering, with delivery and POST answers
// independently gated at the browser boundary.
func TestPendingPostBrowser(t *testing.T) {
	t.Parallel()
	browser := launchBrowser(t, t.Context(), "pending posts")
	for _, topic := range []bool{false, true} {
		t.Run(map[bool]string{false: "feed", true: "topic"}[topic], func(t *testing.T) {
			testPendingPostPage(t, browser, topic)
			testPendingQueuePostPage(t, browser, topic)
			testPendingEditPostPage(t, browser, topic)
		})
	}
}

func testPendingPostPage(t *testing.T, browser *rod.Browser, topic bool) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "replacement", true: "lost-response"}[lost], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
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
			waitPendingPost(t, page, "initial read completion", `() => !document.querySelector('#read-form.htmx-request')`)
			before := page.MustEval(`() => {const s=document.getElementById('organization-stream');return JSON.stringify([s.dataset.eventCursor,s.dataset.appliedCursor]);}`).Str()
			initialReads := reads.Load()
			sent, release := make(chan url.Values, 2), make(chan struct{}, 2)
			router := page.HijackRequests()
			var attempt atomic.Int32
			router.MustAdd(server.URL+path, func(h *rod.Hijack) {
				values, _ := url.ParseQuery(h.Request.Body())
				select {
				case sent <- values:
				case <-ctx.Done():
					return
				}
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
			defer func() {
				cancel()
				_ = router.Stop()
			}()
			page.MustEval(`() => {const b=document.getElementById('message-body'); window.cursor=document.getElementById('organization-stream').dataset.eventCursor; b.value='pending database post'; b.form.requestSubmit();}`)
			values := receivePendingPost(t, ctx, sent, "initial POST")
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
			waitPendingPost(t, page, "initial POST answer", `() => window.answers===1`)
			assertPhase()
			if lost {
				page.MustElement("[data-pending-actions] button[type=submit]").MustClick()
				retry := receivePendingPost(t, ctx, sent, "retry POST")
				if retry.Get("idempotency_key") != values.Get("idempotency_key") || retry.Get("body") != values.Get("body") {
					t.Fatal("retry changed text/key")
				}
				release <- struct{}{}
				waitPendingPost(t, page, "retry answer", `() => window.answers===2`)
				assertPhase()
			}
			waitPendingPost(t, page, "held stream delivery", `() => !!window.delivery`)
			page.MustEval(`() => { window.holding=false; const old=window.delivery; document.getElementById('message-items').dispatchEvent(new CustomEvent('htmx:sseBeforeMessage',{bubbles:true,cancelable:true,detail:old.detail})); }`)
			waitPendingPost(t, page, "stream replacement and read completion", `before => {const s=document.getElementById('organization-stream');return !document.querySelector('#message-pending > li') && !!s.dataset.appliedCursor && s.dataset.eventCursor===s.dataset.appliedCursor && JSON.stringify([s.dataset.eventCursor,s.dataset.appliedCursor])!==before && !document.querySelector('#read-form.htmx-request');}`, before)
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

func testPendingQueuePostPage(t *testing.T, browser *rod.Browser, topic bool) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprintf("queue/lost=%t", lost), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			// The queue assertions count the entire history; feedFixture seeds five posts.
			pool := pgtest.New(t)
			f := conversationtest.OrganizationWithOwner(t, pool, "feed", "general")
			handler, sessions, err := buildHandler(ctx, pool, handlerConfig{maxChannels: conversation.DefaultMaxChannels, hub: realtime.NewHub()})
			feedRequire(t, err)
			var account kernel.ID
			feedRequire(t, pool.QueryRow(ctx, "SELECT account_id FROM member WHERE organization_id=$1 AND id=$2", f.OrganizationID, f.MemberID).Scan(&account))
			token, _, err := sessions.Create(ctx, account)
			feedRequire(t, err)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: token})
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			path := view.ChannelURL("feed", f.Channel.ID)
			if topic {
				place := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "queue")
				path = view.ConversationURL("feed", f.Channel.ID, &place.ID)
			}
			page, closePage := boundedPage(t, browser, ctx, "queued posts")
			defer closePage()
			page.MustEvalOnNewDocument(`(() => {window.answers=0;document.addEventListener('htmx:afterRequest',e=>{if(e.detail.requestConfig?.elt.matches('[data-pending-form]')) window.answers++;});})()`)
			page.MustNavigate(server.URL + path).MustWaitLoad()
			sent, release := make(chan url.Values, 4), make(chan struct{}, 4)
			router := page.HijackRequests()
			var attempts atomic.Int32
			router.MustAdd(server.URL+path, func(h *rod.Hijack) {
				if h.Request.Method() != "POST" {
					h.ContinueRequest(&proto.FetchContinueRequest{})
					return
				}
				values, _ := url.ParseQuery(h.Request.Body())
				select {
				case sent <- values:
				case <-ctx.Done():
					return
				}
				n := attempts.Add(1)
				if err := h.LoadResponse(http.DefaultClient, true); err != nil {
					t.Error(err)
				}
				select {
				case <-release:
				case <-ctx.Done():
				}
				if lost && n == 2 {
					h.Response.Fail(proto.NetworkErrorReasonConnectionReset)
				}
			})
			go router.Run()
			defer func() {
				cancel()
				_ = router.Stop()
			}()
			page.MustEval(`() => {const b=document.getElementById('message-body');for(const text of ['queue 1','queue 2','queue 3']) {b.value=text;b.form.requestSubmit();}}`)

			for i := 1; i <= 3; i++ {
				values := receivePendingPost(t, ctx, sent, fmt.Sprintf("queue POST %d", i))
				if values.Get("body") != fmt.Sprintf("queue %d", i) || len(sent) != 0 {
					t.Fatal("POST order or serialization differs")
				}
				release <- struct{}{}
				answers := i
				if lost && i == 3 {
					answers++
				}
				waitPendingPost(t, page, fmt.Sprintf("queue answer %d", answers), `n => window.answers===n`, answers)
				if lost && i == 2 {
					if attempts.Load() != 2 || len(sent) != 0 {
						t.Fatal("third POST escaped the unknown-outcome pause")
					}
					page.MustElement("[data-pending-actions] button[type=submit]").MustClick()
					retry := receivePendingPost(t, ctx, sent, "second POST retry")
					if retry.Get("idempotency_key") != values.Get("idempotency_key") || retry.Get("body") != values.Get("body") {
						t.Fatal("same-key retry changed the second send")
					}
					release <- struct{}{}
					waitPendingPost(t, page, "second POST retry answer", `() => window.answers===3`)
				}
			}
			waitPendingPost(t, page, "three delivered messages and no pending items", `() => !document.querySelector('#message-pending > li') && document.querySelectorAll('#message-items > li').length===3`)
			rows, err := pool.Query(ctx, "SELECT id, body FROM message WHERE organization_id=$1 AND channel_id=$2 ORDER BY event_seq", f.OrganizationID, f.Channel.ID)
			feedRequire(t, err)
			defer rows.Close()
			n := 0
			for rows.Next() {
				var id kernel.ID
				var body string
				feedRequire(t, rows.Scan(&id, &body))
				n++
				if body != fmt.Sprintf("queue %d", n) || page.MustElement("#body-"+view.MessageDOMID(id)).MustText() != body || page.MustEval(`id => document.querySelectorAll('#'+id).length!==1`, view.MessageDOMID(id)).Bool() {
					t.Fatal("database event_seq order or visible identity differs")
				}
			}
			feedRequire(t, rows.Err())
			if n != 3 {
				t.Fatalf("stored %d messages, want 3", n)
			}
		})
	}
}

func receivePendingPost(t *testing.T, ctx context.Context, sent <-chan url.Values, what string) url.Values {
	t.Helper()
	select {
	case values := <-sent:
		return values
	case <-ctx.Done():
		t.Fatalf("waiting for %s: %v", what, ctx.Err())
		return nil
	}
}

func waitPendingPost(t *testing.T, page *rod.Page, what, predicate string, args ...any) {
	t.Helper()
	if err := page.Wait(rod.Eval(predicate, args...)); err != nil {
		t.Fatalf("waiting for %s: %v", what, err)
	}
}

func testPendingEditPostPage(t *testing.T, browser *rod.Browser, topic bool) {
	for _, mode := range []string{"equivalent", "normalized", "changed", "unused"} {
		t.Run("edit/"+mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			pool := pgtest.New(t)
			f := conversationtest.OrganizationWithOwner(t, pool, "feed", "general")
			handler, sessions, err := buildHandler(ctx, pool, handlerConfig{maxChannels: conversation.DefaultMaxChannels, hub: realtime.NewHub()})
			feedRequire(t, err)
			var account kernel.ID
			feedRequire(t, pool.QueryRow(ctx, "SELECT account_id FROM member WHERE organization_id=$1 AND id=$2", f.OrganizationID, f.MemberID).Scan(&account))
			token, _, err := sessions.Create(ctx, account)
			feedRequire(t, err)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: token})
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			path := view.ChannelURL("feed", f.Channel.ID)
			if topic {
				place := conversationtest.Topic(t, pool, f.OrganizationID, f.Channel.ID, "editing")
				path = view.ConversationURL("feed", f.Channel.ID, &place.ID)
			}
			page, closePage := boundedPage(t, browser, ctx, "edit pending post")
			defer closePage()
			page.MustEvalOnNewDocument(`(() => {window.answers=0;window.holding=true;window.deliveries=[];
 document.addEventListener('htmx:sseBeforeMessage',e=>{if(window.holding && e.target.id==='message-items'){e.stopImmediatePropagation();e.preventDefault();window.deliveries.push(e.detail);}},true);
 document.addEventListener('htmx:afterRequest',e=>{if(e.detail.requestConfig?.elt.matches('[data-pending-form]')){window.answers++;window.postStatus=e.detail.xhr.status;window.result=new DOMParser().parseFromString(e.detail.xhr.responseText,'text/html').querySelector('#message-composer-state')?.dataset.postedMessage;}});
})()`)
			page.MustNavigate(server.URL + path).MustWaitLoad()
			sent := make(chan url.Values, 3)
			router := page.HijackRequests()
			var attempts atomic.Int32
			router.MustAdd(server.URL+path, func(h *rod.Hijack) {
				if h.Request.Method() != "POST" {
					h.ContinueRequest(&proto.FetchContinueRequest{})
					return
				}
				values, _ := url.ParseQuery(h.Request.Body())
				select {
				case sent <- values:
				case <-ctx.Done():
					return
				}
				if err := h.LoadResponse(http.DefaultClient, true); err != nil {
					t.Error(err)
				}
				if attempts.Add(1) == 1 && mode != "unused" {
					h.Response.Fail(proto.NetworkErrorReasonConnectionReset)
				}
			})
			go router.Run()
			defer func() { cancel(); _ = router.Stop() }()
			original, edited := "original", "original"
			switch mode {
			case "unused":
				original, edited = "bad\u0001", "corrected"
			case "normalized":
				edited = " \noriginal \n"
			case "changed":
				edited = "changed"
			}
			page.MustEval(`text=>{const b=document.getElementById('message-body');b.value=text;b.form.requestSubmit();}`, original)
			first := receivePendingPost(t, ctx, sent, "first POST")
			waitPendingPost(t, page, "failed first answer", `()=>window.answers===1`)
			name := "unconfirmed"
			if mode == "unused" {
				name = "notSent"
			}
			if page.MustEval(`name=>document.querySelector('[data-pending-form] [role=status]').textContent!==document.getElementById('message-send-alert').dataset[name]`, name).Bool() {
				t.Fatal("first answer did not show the expected failure")
			}
			page.MustElement("[data-pending-edit]").MustClick()
			page.MustEval(`text=>{const b=document.getElementById('message-body');b.value=text;b.dispatchEvent(new Event('input',{bubbles:true}));b.form.requestSubmit();b.value=text+' next draft';window.draft=b.value;}`, edited)
			resend := receivePendingPost(t, ctx, sent, "edited POST")
			if resend.Get("idempotency_key") != first.Get("idempotency_key") || resend.Get("body") != edited {
				t.Fatal("Edit resend changed the original key or text")
			}
			waitPendingPost(t, page, "edited answer", `()=>window.answers===2`)
			answers, want := 2, 1
			if mode == "changed" {
				if page.MustEval(`()=>window.postStatus!==409 || document.querySelector('[data-pending-form]').dataset.state!=='failed' || document.querySelector('[data-pending-form] [role=status]').textContent!==document.getElementById('message-send-alert').dataset.repeat`).Bool() {
					t.Fatal("changed text under a used key did not show the conflict")
				}
				page.MustElement("[data-pending-actions] button[type=submit]").MustClick()
				resend = receivePendingPost(t, ctx, sent, "fresh-key retry")
				if resend.Get("idempotency_key") == first.Get("idempotency_key") || resend.Get("body") != edited {
					t.Fatal("conflict Retry did not use a fresh key with the changed text")
				}
				answers, want = 3, 2
				waitPendingPost(t, page, "retry answer", `()=>window.answers===3`)
			}
			if page.MustEval(`n=>window.answers!==n || window.postStatus!==200 || document.getElementById('message-body').value!==window.draft`, answers).Bool() {
				t.Fatal("pending response changed the composer or failed")
			}
			waitPendingPost(t, page, "held deliveries", `n=>window.deliveries.length===n`, want)
			page.MustEval(`()=>{window.holding=false;for(const detail of window.deliveries)document.getElementById('message-items').dispatchEvent(new CustomEvent('htmx:sseBeforeMessage',{bubbles:true,cancelable:true,detail}));}`)
			waitPendingPost(t, page, "replacement", `n=>!document.querySelector('#message-pending > li') && document.querySelectorAll('#message-items > li').length===n`, want)
			rows, err := pool.Query(ctx, "SELECT id, body, idempotency_key::text FROM message WHERE organization_id=$1 AND channel_id=$2 ORDER BY event_seq", f.OrganizationID, f.Channel.ID)
			feedRequire(t, err)
			defer rows.Close()
			n := 0
			for rows.Next() {
				var id kernel.ID
				var body, key string
				feedRequire(t, rows.Scan(&id, &body, &key))
				n++
				text, expectedKey := "original", first.Get("idempotency_key")
				if mode == "unused" || n == 2 {
					text, expectedKey = edited, resend.Get("idempotency_key")
				}
				if body != text || key != expectedKey || page.MustElement("#body-"+view.MessageDOMID(id)).MustText() != text || page.MustEval(`id=>document.querySelectorAll('#message-items > #'+id).length!==1`, view.MessageDOMID(id)).Bool() {
					t.Fatal("stored or visible body, key or identity differs")
				}
				if n == want && page.MustEval(`()=>window.result`).Str() != view.MessageDOMID(id) {
					t.Fatal("resend answer did not name the stored message")
				}
			}
			feedRequire(t, rows.Err())
			if n != want {
				t.Fatalf("stored %d messages, want %d", n, want)
			}
		})
	}
}
