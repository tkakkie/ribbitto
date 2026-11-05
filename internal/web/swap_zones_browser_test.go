package web

import (
	"net/http"
	"testing"

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// Keep real server fragments and htmx targets while gating only transport.
func swapZonePage(t *testing.T, browser *rod.Browser, panel string) (*rod.Page, chan realtime.Outgoing, chan pendingRequest) {
	t.Helper()
	page, events, posts, _ := pendingBrowserFixture(t, browser, false, func(p *view.ChannelPage, mux *http.ServeMux) {
		p.Panel, p.Older = panel, true
		p.Messages = streamTestMessages(kernel.ID{2}, 40)
		p.Messages[0].URL = view.MessageURL("acme", p.Messages[0].ID)
		p.Messages[0].PinURL = view.PinURL("acme", p.Current.ID, p.Messages[0].ID)
		p.Members = &view.MemberPage{Items: []view.Member{{ID: kernel.ID{3}, Handle: "alice"}}}
		mux.HandleFunc("GET /organizations/acme/mention-candidates", func(w http.ResponseWriter, r *http.Request) {
			templ.Handler(view.MentionCandidates(r.URL.Query().Get("q"), p.Members.Items)).ServeHTTP(w, r)
		})
		mux.HandleFunc("GET /organizations/acme/channels/{channel}", func(w http.ResponseWriter, r *http.Request) {
			older := *p
			older.Messages, older.Older = streamTestMessages(kernel.ID{2}, 20), false
			templ.Handler(view.ChannelScreen("/static/css/app.css", older)).ServeHTTP(w, r)
		})
	})
	return page, events, posts
}

func rememberSwapState(page *rod.Page) {
	page.MustEval(`() => {
  window.keptNodes=['message-body','message-pending','message-composer','organization-stream'].map(id=>document.getElementById(id));
  const b=keptNodes[0]; b.value='draft @ali text'; b.focus(); b.setSelectionRange(3,8,'backward');
  window.keptText=JSON.stringify([b.value,b.selectionStart,b.selectionEnd,b.selectionDirection]);
 }`)
}

func assertSwapState(t *testing.T, page *rod.Page) {
	t.Helper()
	if !page.MustEval(`() => {
  const b=document.getElementById('message-body');
  return keptNodes.every(n=>n && document.getElementById(n.id)===n) &&
   keptText===JSON.stringify([b.value,b.selectionStart,b.selectionEnd,b.selectionDirection]);
 }`).Bool() {
		t.Fatal("swap replaced a protected node or changed typed text/selection")
	}
}

func swapSidebar(t *testing.T, page *rod.Page, events chan realtime.Outgoing) {
	t.Helper()
	page.MustEval(`() => {window.oldSidebar=document.getElementById('sidebar-channels');}`)
	events <- realtime.Outgoing{Ephemeral: true, Name: "sidebar", Data: streamTestMarkup(t, view.SidebarLists(view.ChannelPage{Organization: view.Organization{Slug: "acme"}, Current: view.Channel{ID: kernel.ID{1}}}))}
	page.MustWait(`() => document.getElementById('sidebar-channels')!==oldSidebar`)
}

func testSwapZonesBrowser(t *testing.T, browser *rod.Browser) {
	for _, tc := range []struct{ name, panel, target string }{
		{"sidebar", "overview", "sidebar-channels"}, {"presence", "members", view.PresenceDOMID(kernel.ID{3})},
		{"typing", "overview", "typing-status"}, {"pins", "overview", "pins-section"},
		{"message", "overview", "message-items"}, {"composer", "overview", "message-composer-state"},
		{"quote", "overview", "composer-quote"}, {"mention", "overview", "mention-listbox"},
		{"pins-ajax", "overview", "pin-form-message-28000000000000000000000000000000"}, {"older", "overview", "load-older"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, events, posts := swapZonePage(t, browser, tc.panel)
			var request pendingRequest
			if tc.name == "composer" {
				page.MustEval(`() => document.getElementById('quote-form').requestSubmit()`)
				receivePending(t, page, posts).reply <- browserReply{status: 200, body: streamTestMarkup(t, view.ComposerQuote(view.ChannelPage{Quote: &view.Message{ID: kernel.ID{40}, Body: "original", Handle: "bob"}}, false))}
				page.MustWait(`() => !!document.querySelector('#composer-quote input[name=quote]')`)
				page.MustEval(`() => {const b=document.getElementById('message-body');b.value='sent';b.form.requestSubmit()}`)
				request = receivePending(t, page, posts)
			}
			if tc.name == "pins-ajax" {
				messageMenu(kernel.ID{40}).open(page)
			}
			rememberSwapState(page)
			page.MustEval(`id => {window.oldZone=document.getElementById(id);window.oldContent=oldZone.innerHTML;}`, tc.target)
			switch tc.name {
			case "sidebar":
				swapSidebar(t, page, events)
			case "presence":
				events <- realtime.Outgoing{Ephemeral: true, Name: "presence", Data: streamTestMarkup(t, view.PresenceIndicator(kernel.ID{3}, true, true))}
			case "typing":
				events <- realtime.Outgoing{Ephemeral: true, Name: "typing", Data: streamTestMarkup(t, view.TypingIndicator(typing.Snapshot{Typists: []typing.Typist{{Handle: "bob"}}}))}
			case "pins":
				events <- realtime.Outgoing{Ephemeral: true, Name: "pins", Data: streamTestMarkup(t, view.PinsSection(view.ChannelPage{SnapshotSeq: 43}))}
			case "message":
				events <- realtime.Outgoing{ID: 51, Name: "message", Data: streamTestMarkup(t, view.LiveMessageItem(streamTestMessages(kernel.ID{2}, 51)[0]))}
			case "composer":
				answer := view.ChannelPage{Composer: new("fresh"), IdempotencyKey: new("server"), QuoteUpdate: true, PostedMessageID: new(kernel.ID{51}), PostedTopicID: kernel.ID{2}, PostedEventSeq: 51}
				page.MustEval(`() => {window.oldOOB=['typing-form','composer-quote'].map(id=>document.getElementById(id));}`)
				request.reply <- browserReply{status: 200, body: streamTestMarkup(t, view.MessageComposer(answer))}
			case "quote":
				page.MustEval(`() => document.getElementById('quote-form').requestSubmit()`)
				receivePending(t, page, posts).reply <- browserReply{status: 200, body: streamTestMarkup(t, view.ComposerQuote(view.ChannelPage{Quote: &view.Message{ID: kernel.ID{40}, Body: "original", Handle: "bob"}}, false))}
			case "mention":
				page.MustEval(`() => {const f=document.getElementById('mention-search');f.elements.q.value='ali';htmx.trigger(f,'mention-query')}`)
			case "pins-ajax":
				page.MustEval(`() => document.querySelector('[data-pin-control]').requestSubmit(document.querySelector('[data-pin-action="false"]'))`)
				entry := streamTestMessages(kernel.ID{2}, 40)[0]
				entry.Pinned = true
				receivePending(t, page, posts).reply <- browserReply{status: 200, body: streamTestMarkup(t, view.PinResponse(entry, "", view.ChannelPage{SnapshotSeq: 43}, true))}
			case "older":
				page.MustEval(`() => document.querySelector('#load-older a').click()`)
			}
			page.MustWait(`id => {const n=document.getElementById(id);return n && (n!==oldZone || n.innerHTML!==oldContent)}`, tc.target)
			if tc.name == "composer" {
				page.MustWait(`() => oldOOB.every(n=>document.getElementById(n.id)!==n)`)
			}
			assertSwapState(t, page)
		})
	}
}

func testSwapPendingUnknownBrowser(t *testing.T, browser *rod.Browser) {
	page, events, posts := swapZonePage(t, browser, "overview")
	page.MustEval(`() => {const b=document.getElementById('message-body');for(const text of ['unknown','queued']){b.value=text;b.form.requestSubmit()}}`)
	receivePending(t, page, posts).reply <- browserReply{status: 500, body: []byte("unknown")}
	page.MustWait(`() => window.answers===1`)
	rememberSwapState(page)
	page.MustEval(`() => {window.queueBefore=[...document.querySelectorAll('[data-pending-form]')].map(f=>[f,f.dataset.state,f.elements.idempotency_key.value,f.hasAttribute('data-unconfirmed'),f.elements.body.value,f.previousElementSibling.textContent]);}`)
	// A normal resync is acknowledged over HTTP, then delivered as a sidebar frame.
	page.MustEval(`() => htmx.trigger(document.getElementById('sidebar-resync'),'sidebar-resync')`)
	receivePending(t, page, posts).reply <- browserReply{status: 204}
	swapSidebar(t, page, events)
	if !page.MustEval(`() => queueBefore.length===2 && queueBefore[0][1]==='failed' && queueBefore[0][3] && queueBefore[1][1]==='queued' && queueBefore.every(([f,s,k,u,text,shown])=>f.isConnected && f.dataset.state===s && f.elements.idempotency_key.value===k && f.hasAttribute('data-unconfirmed')===u && f.elements.body.value===text && f.previousElementSibling.textContent===shown) && requests===1`).Bool() {
		t.Fatal("resync changed the paused queue's states, uncertainty or keys")
	}
	assertSwapState(t, page)
}

func testSwapPendingNotSentBrowser(t *testing.T, browser *rod.Browser) {
	page, _, posts := swapZonePage(t, browser, "overview")
	page.MustEval(`() => {const b=document.getElementById('message-body');for(const text of ['not sent','sending','queued']){b.value=text;b.form.requestSubmit()}}`)
	receivePending(t, page, posts).reply <- browserReply{status: 400, body: []byte("not sent")}
	second := receivePending(t, page, posts)
	rememberSwapState(page)
	page.MustEval(`() => {window.queueBefore=[...document.querySelectorAll('[data-pending-form]')].filter(f=>f.dataset.state!=='sending').map(f=>[f,f.dataset.state,f.elements.idempotency_key.value,f.elements.body.value,f.previousElementSibling.textContent]);}`)
	second.reply <- pendingAnswer(t, kernel.ID{51})
	third := receivePending(t, page, posts)
	if !page.MustEval(`() => queueBefore.length===2 && queueBefore[0][1]==='failed' && !queueBefore[0][0].hasAttribute('data-unconfirmed') && queueBefore[1][1]==='queued' && queueBefore.every(([f,s,k,text,shown],i)=>f.isConnected && f.dataset.state===(i?'sending':s) && f.elements.idempotency_key.value===k && f.elements.body.value===text && f.previousElementSibling.textContent===shown)`).Bool() {
		t.Fatal("another response lost the not-sent item or the queued item's key/FIFO transition")
	}
	assertSwapState(t, page)
	third.reply <- browserReply{status: 500, body: []byte("pause")}
	page.MustWait(`() => window.answers===3`)
}

func testSwapIMEBrowser(t *testing.T, browser *rod.Browser) {
	page, events, posts := swapZonePage(t, browser, "overview")
	page.MustEval(`() => {crypto.randomUUID=undefined;const b=document.getElementById('message-body');b.value='sent';b.focus();b.setSelectionRange(4,4);b.form.requestSubmit();window.stateBefore=document.getElementById('message-composer-state')}`)
	request := receivePending(t, page, posts)
	if err := (proto.InputImeSetComposition{Text: "に", SelectionStart: 1, SelectionEnd: 1}).Call(page); err != nil {
		t.Fatal(err)
	}
	swapSidebar(t, page, events)
	request.reply <- pendingAnswer(t, kernel.ID{51})
	page.MustWait(`() => document.getElementById('message-composer-state')!==stateBefore`)
	if got := page.MustEval(`() => document.getElementById('message-body').value`).Str(); got != "sentに" {
		t.Fatalf("mid-IME response removed sent text too early: %q", got)
	}
	if err := (proto.InputInsertText{Text: "日本"}).Call(page); err != nil {
		t.Fatal(err)
	}
	page.MustWait(`() => document.getElementById('message-body').value==='日本'`)
}
