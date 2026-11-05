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
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

// Characterization: only approved A-F work may change these expectations.
// Native EventSource remembers receipt even when the DOM application fails.
func testCharacterizationFailedMessageReconnect(t *testing.T, browser *rod.Browser) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	snapshot := view.ChannelPage{Organization: view.Organization{Slug: "acme"}, Current: view.Channel{ID: kernel.ID{1}}, EventCursor: new(int64(42))}
	requests := make(chan string, 3)
	frames := make(chan *realtime.Outgoing)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/organizations/acme/events" {
			templ.Handler(view.ChannelScreen("/static/css/app.css", snapshot)).ServeHTTP(w, r)
			return
		}
		after, ok := streamCursor(r)
		if !ok {
			t.Error("invalid stream cursor")
			return
		}
		requests <- fmt.Sprintf("%s/%s/%d", r.URL.Query().Get("after"), r.Header.Get("Last-Event-ID"), after)
		w.Header().Set("Content-Type", "text/event-stream")
		sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
		if sender.Heartbeat(r.Context()) != nil {
			return
		}
		// Model the server's exclusive replay boundary using the real cursor parser.
		if after < 51 && sender.Send(r.Context(), realtime.Outgoing{ID: 51, Name: "message", Data: streamTestMarkup(t, view.LiveMessageItem(streamTestMessages(kernel.ID{2}, 51)[0]))}) != nil {
			return
		}
		for {
			select {
			case frame := <-frames:
				if frame == nil || sender.Send(r.Context(), *frame) != nil {
					return
				}
			case <-r.Context().Done():
				return
			}
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	page := browser.MustPage().Context(ctx).WithPanic(failAtTestLine(t))
	defer page.MustClose()
	page.MustEvalOnNewDocument(`window.deliveries=0; window.faults=0;
 document.addEventListener('htmx:sseOpen',e=>{window.source=e.detail.source;
 e.detail.source.addEventListener('message',()=>window.deliveries++);
 e.detail.source.addEventListener('replay-complete',()=>window.replayComplete=true);});
 document.addEventListener('DOMContentLoaded',()=>{
 const items=document.getElementById('message-items'); const insert=items.insertBefore;
 items.insertBefore=function(node,before){if(node.nodeType===1 && node.dataset.eventSeq==='51'){window.faults++;throw Error('test application failure')}return insert.call(this,node,before)};
 });`)
	page.MustNavigate(server.URL).MustWaitLoad()
	request := func(want string) {
		t.Helper()
		select {
		case got := <-requests:
			if got != want {
				t.Fatalf("request=%s, want %s", got, want)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	send := func(frame *realtime.Outgoing) {
		t.Helper()
		select {
		case frames <- frame:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	request("42//42")
	page.MustWait(`()=>window.deliveries===1 && window.faults===1`)
	assertBrowserItems(t, page, nil)
	if !page.MustEval(`()=>{const s=document.getElementById('organization-stream');return !s.dataset.appliedCursor && s.dataset.eventCursor==='42' && new URL(s.getAttribute('sse-connect'),location.href).searchParams.get('after')==='42'}`).Bool() {
		t.Fatal("failed application advanced page cursors")
	}
	send(nil)
	request("42/51/51")
	// An id-less marker fences replay without changing any durable cursor.
	send(&realtime.Outgoing{Ephemeral: true, Name: "replay-complete"})
	page.MustWait(`()=>window.replayComplete===true`)
	assertBrowserItems(t, page, nil)
	if page.MustEval(`()=>window.deliveries`).Int() != 1 {
		t.Fatal("failed frame replayed")
	}
	// Characterization: a later application advances past the failed frame.
	send(&realtime.Outgoing{ID: 52, Name: "message", Data: streamTestMarkup(t, view.LiveMessageItem(streamTestMessages(kernel.ID{2}, 52)[0]))})
	page.MustWait(`()=>document.getElementById('organization-stream').dataset.appliedCursor==='52'`)
	assertBrowserItems(t, page, []int{52})
}

func testDuplicateReconnectBrowser(t *testing.T, browser *rod.Browser) {
	page, events, posts, _ := pendingBrowser(t, browser, false)
	page.MustEval(`()=>{const b=document.getElementById('message-body');b.value='replayed post';b.form.requestSubmit()}`)
	post := receivePending(t, page, posts)
	post.reply <- pendingAnswer(t, kernel.ID{51})
	page.MustWait(`()=>document.querySelector('[data-pending-form]')?.dataset.state==='confirmed'`)
	frame := realtime.Outgoing{ID: 51, Name: "message", Data: streamTestMarkup(t, view.LiveMessageItem(streamTestMessages(kernel.ID{2}, 51)[0]))}
	events <- frame
	page.MustWait(`()=>!document.querySelector('#message-pending > li') && document.getElementById('organization-stream').dataset.appliedCursor==='51'`)
	page.MustEval(`()=>{window.deliveries=0;document.addEventListener('htmx:sseBeforeMessage',()=>window.deliveries++);window.source.close();window.source.dispatchEvent(new Event('error'))}`)
	page.MustWait(`()=>window.opens===2`)
	events <- frame
	page.MustWait(`()=>window.deliveries===1`)
	assertBrowserItems(t, page, []int{51})
	if !page.MustEval(`()=>document.getElementById('message-status').childNodes.length===1 && !document.querySelector('#message-pending > li')`).Bool() {
		t.Fatal("replay duplicated announcement or pending item")
	}
}
