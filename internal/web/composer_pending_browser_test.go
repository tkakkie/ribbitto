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

	"github.com/a-h/templ"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"github.com/tkakkie/ribbitto/web/static"
)

type pendingRequest struct {
	values url.Values
	reply  chan browserReply
}

// Real markup, htmx and stream script; only transport delivery is gated.
func pendingBrowser(t *testing.T, browser *rod.Browser) (*rod.Page, chan realtime.Outgoing, chan pendingRequest, *atomic.Int32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	snapshot := view.ChannelPage{Organization: view.Organization{Slug: "acme"}, Current: view.Channel{ID: kernel.ID{1}}, MemberID: kernel.ID{3}, Handle: "alice", DisplayName: "Alice", EventCursor: new(int64(42)), Composer: new("generation"), IdempotencyKey: new("00000000-0000-4000-8000-000000000001"), Reading: true}
	events, posts := make(chan realtime.Outgoing, 8), make(chan pendingRequest, 8)
	var reads atomic.Int32
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(static.FS())))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/events"):
			w.Header().Set("Content-Type", "text/event-stream")
			sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
			for err := sender.Heartbeat(r.Context()); err == nil; {
				select {
				case out := <-events:
					err = sender.Send(r.Context(), out)
				case <-r.Context().Done():
					return
				}
			}
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/read"):
			reads.Add(1)
			w.WriteHeader(204)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/typing"):
			w.WriteHeader(204)
		case r.Method == "POST":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				return
			}
			request := pendingRequest{r.PostForm, make(chan browserReply, 1)}
			posts <- request
			select {
			case reply := <-request.reply:
				w.WriteHeader(reply.status)
				_, _ = w.Write(reply.body)
			case <-r.Context().Done():
			}
		default:
			templ.Handler(view.ChannelScreen("/static/css/app.css", snapshot)).ServeHTTP(w, r)
		}
	})
	catalogues, err := i18n.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(catalogues.Middleware(mux))
	t.Cleanup(server.Close)
	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	t.Cleanup(func() { page.Context(context.Background()).Timeout(5 * time.Second).MustClose() })
	page.MustEvalOnNewDocument(`(() => {
 window.answers=0; window.opens=0; window.requests=0; window.failures=[];
 document.addEventListener('DOMContentLoaded',()=>new MutationObserver(records=>{
  if(records.some(r=>r.target.matches?.('[data-pending-form] [role=status]'))) {
   const text=document.querySelector('[data-pending-form] [role=status]')?.textContent;
   if(text) window.failures.push(text);
  }
 }).observe(document.getElementById('message-pending'),{childList:true,subtree:true}));
 document.addEventListener('htmx:beforeSend',e=>{if(e.detail.requestConfig?.elt.matches('[data-pending-form]')) window.requests++;});
 document.addEventListener('htmx:afterRequest', e => { if(e.detail.requestConfig?.elt.matches('[data-pending-form]')) window.answers++; });
 document.addEventListener('htmx:sseOpen', e => { window.source=e.detail.source; window.opens++; });
 })()`)
	page.MustNavigate(server.URL).MustWaitLoad()
	page.MustWait(`() => window.source && !document.querySelector('#read-form.htmx-request')`)
	return page, events, posts, &reads
}

func pendingAnswer(t *testing.T, id kernel.ID) browserReply {
	t.Helper()
	return browserReply{status: 200, body: []byte(renderIn(t, "en", view.MessageComposer(view.ChannelPage{PostedMessageID: &id, Composer: new("fresh"), IdempotencyKey: new("00000000-0000-4000-8000-000000000002"), EventCursor: new(int64(999))})))}
}

func TestPendingTemplateScope(t *testing.T) {
	for _, tc := range []struct {
		name           string
		before, around int64
		topic          *view.Topic
		want           bool
	}{
		{"feed", 0, 0, nil, true}, {"older", 40, 0, nil, false}, {"around", 0, 40, nil, false}, {"topic", 0, 0, &view.Topic{ID: kernel.ID{2}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			markup := string([]byte(renderIn(t, "en", view.ChannelScreen("/static/css/app.css", view.ChannelPage{Before: tc.before, Around: tc.around, Topic: tc.topic}))))
			for _, id := range []string{"message-pending", "message-pending-template"} {
				if strings.Contains(markup, `id="`+id+`"`) != tc.want {
					t.Fatalf("%s scope", id)
				}
			}
		})
	}
}

func testPendingComposerBrowser(t *testing.T, browser *rod.Browser) {
	for _, width := range []int{1280, 390} {
		for _, first := range []string{"response", "stream"} {
			for _, space := range []string{"\u3000", "\ufeff"} {
				t.Run(fmt.Sprintf("%d/%s/%x", width, first, space), func(t *testing.T) {
					page, events, posts, reads := pendingBrowser(t, browser)
					page.MustSetViewport(width, 800, 1, false)
					initialReads := reads.Load()
					page.MustEval(`space => {
   const b=document.getElementById('message-body'); b.value=space+'<b>x</b>'+space; b.focus();
   window.oldKey=b.form.elements.idempotency_key.value;
   b.form.requestSubmit();
   window.frames=[]; const frame=()=>{ window.frames.push(document.querySelectorAll('#message-items > #message-33000000000000000000000000000000, #message-pending > li').length); window.frame=requestAnimationFrame(frame); }; frame();
  }`, space)
					var request pendingRequest
					select {
					case request = <-posts:
					case <-page.GetContext().Done():
						t.Fatal("pending request did not arrive")
					}
					if request.values.Get("body") != space+"<b>x</b>"+space || request.values.Get("cursor") != "42" || request.values.Get("composer") != "generation" {
						t.Fatalf("pending controls: %v", request.values)
					}
					if problem := page.MustEval(`() => {
   const b=document.getElementById('message-body'), p=document.getElementById('message-pending-body'), s=document.getElementById('organization-stream');
   if(document.querySelector('[data-pending-sending]').textContent!=='Sending…') return 'unlocalised sending';
   if(p.textContent!=='<b>x</b>' || p.children.length || b.value || document.activeElement!==b || b.form.elements.idempotency_key.value===window.oldKey) return 'text, focus or key';
   if(document.getElementById('message-status').textContent || s.dataset.eventCursor!=='42' || s.dataset.appliedCursor) return 'announcement or cursor';
   if(document.getElementById('message-empty').checkVisibility() || document.querySelector('#message-sending span').checkVisibility()) return 'empty/sending status';
   return '';
  }`).Str(); problem != "" {
						t.Fatal(problem)
					}
					draft := space + "<b>x</b>" + space + "next"
					busy := func() {
						t.Helper()
						page.MustEval(`text => { const b=document.getElementById('message-body'); b.value=text; b.form.requestSubmit(); }`, draft)
						if page.MustEval(`() => window.requests`).Int() != 1 || len(posts) != 0 || page.MustElement("#message-body").MustProperty("value").Str() != draft || page.MustElement("#message-send-alert").MustText() == "" {
							t.Fatal("busy send lost text or started a request")
						}
					}
					busy()
					if first == "response" {
						request.reply <- pendingAnswer(t, kernel.ID{51})
						page.MustWait(`() => window.answers===1`)
						if page.MustElement("#message-body").MustProperty("value").Str() != draft {
							t.Fatal("response changed the next draft")
						}
						if reads.Load() != initialReads || page.MustEval(`() => {const s=document.getElementById('organization-stream'); return s.dataset.eventCursor!=='42' || !!s.dataset.appliedCursor;}`).Bool() {
							t.Fatal("response read or advanced cursors")
						}
						busy()
						page.MustEval(`() => { window.source.close(); window.source.dispatchEvent(new Event('error')); }`)
						page.MustWait(`() => window.opens===2`)
					}
					// Equal text by another member and another tab of this sender must not match.
					own := streamTestMessages(kernel.ID{2}, 51)[0]
					own.MemberID = kernel.ID{3}
					own.DisplayName = "Alice"
					own.Handle = "alice"
					own.Body = strings.TrimSpace(space + "<b>x</b>" + space)
					own.Text = own.Body
					for _, n := range []int{49, 50} {
						collision := own
						collision.ID = kernel.ID{byte(n)}
						collision.EventSeq = int64(n)
						if n == 49 {
							collision.MemberID = kernel.ID{9}
						}
						events <- realtime.Outgoing{ID: int64(n), Name: "message", Data: []byte(renderIn(t, "en", view.LiveMessageItem(collision)))}
					}
					page.MustWait(`() => document.querySelectorAll('#message-items > li').length===2`)
					if !page.MustHas("#message-pending > li") {
						t.Fatal("collision removed pending")
					}
					page.MustEval(`() => { const r=document.getElementById('message-pending-body').getBoundingClientRect(); window.position=[r.x,r.y]; }`)
					out := realtime.Outgoing{ID: 51, Name: "message", Data: []byte(renderIn(t, "en", view.LiveMessageItem(own)))}
					events <- out
					page.MustWait(`() => !!document.querySelector('#message-items > #message-33000000000000000000000000000000')`)
					moved := own
					moved.TopicID = kernel.ID{4}
					events <- realtime.Outgoing{ID: 52, Name: "messages-moved", Data: []byte(renderIn(t, "en", view.MovedMessageItems([]view.Message{moved}, kernel.ID{2}, kernel.ID{4})))}
					page.MustWait(`() => document.getElementById('organization-stream').dataset.eventCursor==='52'`)
					if first == "stream" {
						if !page.MustHas("#message-pending > li") {
							t.Fatal("stream matched without response")
						}
						request.reply <- pendingAnswer(t, own.ID)
					}
					page.MustWait(`() => !document.querySelector('#message-pending > li')`)
					if page.MustElement("#message-body").MustProperty("value").Str() != draft {
						t.Fatal("replacement changed the next draft")
					}
					events <- out // duplicate replay after confirmation must not restore or announce it
					page.MustEval(`() => new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))`)
					if problem := page.MustEval(`first => {
   cancelAnimationFrame(window.frame);
   if(window.frames.some(n=>n<1 || n>(first==='response'?1:2))) return 'frame identity count: '+window.frames;
   const r=document.getElementById('body-message-33000000000000000000000000000000').getBoundingClientRect();
   if(Math.abs(r.x-window.position[0])>1 || Math.abs(r.y-window.position[1])>1) return 'body moved: '+[window.position,r.x,r.y];
   if(document.getElementById('message-status').childNodes.length!==3) return 'announcement count';
   document.getElementById('message-composer').requestSubmit(); return '';
  }`, first).Str(); problem != "" {
						t.Fatal(problem)
					}
					select {
					case next := <-posts:
						next.reply <- pendingAnswer(t, kernel.ID{60})
					case <-page.GetContext().Done():
						t.Fatal("composer stayed blocked after delivery")
					}
				})
			}
		}
	}
}

func testPendingFailureBrowser(t *testing.T, browser *rod.Browser) {
	page, _, posts, _ := pendingBrowser(t, browser)
	answers := make(chan string, 1)
	sent := make(chan url.Values, 1)
	router := page.HijackRequests()
	router.MustAdd("*/organizations/acme/channels/*", func(h *rod.Hijack) {
		if h.Request.Method() != "POST" || strings.HasSuffix(h.Request.URL().Path, "/read") || strings.HasSuffix(h.Request.URL().Path, "/typing") {
			h.ContinueRequest(&proto.FetchContinueRequest{})
			return
		}
		values, _ := url.ParseQuery(h.Request.Body())
		sent <- values
		switch answer := <-answers; answer {
		case "network":
			h.Response.Fail(proto.NetworkErrorReasonConnectionReset)
		case "timeout":
			h.OnError = func(error) {}
			<-page.GetContext().Done()
			h.Response.Fail(proto.NetworkErrorReasonTimedOut)
		default:
			var status int
			_, _ = fmt.Sscan(answer, &status)
			h.Response.Payload().ResponseCode = status
			h.Response.SetBody("refused")
		}
	})
	go router.Run()
	t.Cleanup(func() { _ = router.Stop() })
	page.MustEval(`() => {document.getElementById('message-body').value='kept'; document.getElementById('message-pending-template').content.querySelector('form').setAttribute('hx-request','{"timeout":200}');}`)
	var key string
	for i, answer := range []string{"400", "404", "413", "422", "429", "network", "timeout", "500", "408", "404", "409"} {
		answers <- answer
		page.MustEval(`i => {if(i===0) document.getElementById('message-composer').requestSubmit(); else document.querySelector('[data-pending-form]').requestSubmit();}`, i)
		var values url.Values
		select {
		case values = <-sent:
		case <-page.GetContext().Done():
			t.Fatal("pending retry did not arrive")
		}
		if i == 0 {
			key = values.Get("idempotency_key")
		}
		if values.Get("idempotency_key") != key || values.Get("body") != "kept" {
			t.Fatal("retry changed text/key")
		}
		page.MustWait(`n => window.answers===n`, i+1)
		if page.MustEval(`() => window.failures.length`).Int() != i+1 {
			t.Fatal("failure was not announced once")
		}
		name := "notSent"
		if i >= 5 {
			name = "unconfirmed"
		}
		if i == 9 {
			name = "stillUnconfirmed"
		}
		if i == 10 {
			name = "repeat"
		}
		page.MustEval(`() => { const b=document.getElementById("message-body"); b.value="next draft"; b.form.requestSubmit(); }`)
		if page.MustEval(`() => document.getElementById("message-body").value!=="next draft"`).Bool() || len(sent) != 0 {
			t.Fatal("failed item did not block another send")
		}
		if page.MustEval(`name => document.querySelector('[data-pending-form] [role=status]').textContent!==document.getElementById('message-send-alert').dataset[name] || document.getElementById('message-send-alert').textContent!==document.getElementById('message-send-alert').dataset.busy || document.getElementById('message-body').getAttribute('aria-invalid')!=='false' || document.querySelector('#message-composer [name=composer]').value!=='generation'`, name).Bool() {
			t.Fatalf("failure %s", answer)
		}
	}
	if page.MustEval(`key => document.querySelector('[data-pending-form]').elements.idempotency_key.value===key`, key).Bool() {
		t.Fatal("409 kept stale key")
	}
	page.MustElement("[data-pending-remove]").MustClick()
	page.MustEval(`() => {document.getElementById('message-body').value='next'; document.getElementById('message-composer').requestSubmit();}`)
	answers <- "400"
	<-sent
	page.MustWait(`() => window.answers===12`)
	if len(posts) != 0 {
		t.Fatal("hijacked request escaped")
	}
}
