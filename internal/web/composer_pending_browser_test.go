package web

import (
	"context"
	"fmt"
	"log/slog"
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
	"golang.org/x/net/html"
)

type pendingRequest struct {
	values url.Values
	reply  chan browserReply
}

// Real markup, htmx and stream script; only transport delivery is gated.
func pendingBrowser(t *testing.T, browser *rod.Browser, topic bool) (*rod.Page, chan realtime.Outgoing, chan pendingRequest, *atomic.Int32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	snapshot := view.ChannelPage{Organization: view.Organization{Slug: "acme"}, Current: view.Channel{ID: kernel.ID{1}}, MemberID: kernel.ID{3}, Handle: "alice", DisplayName: "Alice", Role: "member", EventCursor: new(int64(42)), Composer: new("generation"), IdempotencyKey: new("00000000-0000-4000-8000-000000000001"), Reading: true}
	if topic {
		snapshot.Topic = &view.Topic{ID: kernel.ID{2}}
	}
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
	catalogues, err := i18n.New(slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(catalogues.Middleware(mux))
	t.Cleanup(server.Close)
	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	t.Cleanup(func() { page.Context(context.Background()).Timeout(5 * time.Second).MustClose() })
	page.MustEvalOnNewDocument(`(() => {
 window.answers=0; window.opens=0; window.requests=0; window.ordinaryRequests=0; window.failures=[];
 document.addEventListener('DOMContentLoaded',()=>new MutationObserver(records=>{
  if(records.some(r=>r.target.matches?.('[data-pending-form] [role=status]'))) {
   const text=document.querySelector('[data-pending-form] [role=status]')?.textContent;
   if(text) window.failures.push(text);
  }
 }).observe(document.getElementById('message-pending'),{childList:true,subtree:true}));
 document.addEventListener('htmx:beforeSend',e=>{const form=e.detail.requestConfig?.elt; if(form?.matches('[data-pending-form]')) window.requests++; else if(form?.id==='message-composer') window.ordinaryRequests++;});
 document.addEventListener('htmx:afterRequest', e => { if(e.detail.requestConfig?.elt.matches('[data-pending-form]')) window.answers++; });
 document.addEventListener('htmx:sseOpen', e => { window.source=e.detail.source; window.opens++; });
 })()`)
	page.MustNavigate(server.URL).MustWaitLoad()
	page.MustWait(`() => window.source && !document.querySelector('#read-form.htmx-request')`)
	return page, events, posts, &reads
}

func pendingAnswer(t *testing.T, id kernel.ID) browserReply {
	t.Helper()
	return pendingTopicAnswer(t, id, kernel.ID{2})
}

func pendingTopicAnswer(t *testing.T, id, topic kernel.ID) browserReply {
	t.Helper()
	return browserReply{status: 200, body: []byte(renderIn(t, "en", view.MessageComposer(view.ChannelPage{PostedMessageID: &id, PostedTopicID: topic, PostedEventSeq: int64(id[0]), Composer: new("fresh"), IdempotencyKey: new("00000000-0000-4000-8000-000000000002"), EventCursor: new(int64(999))})))}
}

func TestPendingTemplateScope(t *testing.T) {
	for _, tc := range []struct {
		name           string
		before, around int64
		topic          *view.Topic
		want           bool
	}{
		{"feed", 0, 0, nil, true}, {"older", 40, 0, nil, false}, {"around", 0, 40, nil, false}, {"topic", 0, 0, &view.Topic{ID: kernel.ID{2}}, true}, {"topic-older", 40, 0, &view.Topic{ID: kernel.ID{2}}, false}, {"topic-around", 0, 40, &view.Topic{ID: kernel.ID{2}}, false},
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

func TestPendingEditUnavailableReason(t *testing.T) {
	for _, tc := range []struct{ lang, want string }{
		{"en", "Clear the text you are writing to edit this message."},
		{"ja", "編集中の文章を消してから編集してください。"},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			markup := renderIn(t, tc.lang, view.ChannelScreen("/static/css/app.css", view.ChannelPage{}))
			doc, err := html.Parse(strings.NewReader(markup))
			if err != nil {
				t.Fatal(err)
			}
			var reason string
			for node := range doc.Descendants() {
				if _, ok := attrOK(node, "data-pending-edit-help"); ok {
					reason = text(node)
				}
			}
			if reason != tc.want {
				t.Fatalf("pending Edit reason = %q, want %q", reason, tc.want)
			}
		})
	}
}

func testPendingComposerBrowser(t *testing.T, browser *rod.Browser) {
	for _, topic := range []bool{false, true} {
		t.Run(fmt.Sprintf("topic=%t", topic), func(t *testing.T) { testPendingComposerPage(t, browser, topic) })
	}
}

func testPendingComposerPage(t *testing.T, browser *rod.Browser, topic bool) {
	for _, tc := range []struct{ name, text string }{
		{"empty", ""}, {"blank", " \u3000\n"}, {"oversized", strings.Repeat("x", 4001)},
		{"trimmed-limit", strings.Repeat("🙂", 4000) + " \r\n"},
	} {
		for _, state := range []string{"sending", "failed", "confirmed", "removed-in-flight"} {
			t.Run("ordinary-while-pending/"+tc.name+"/"+state, func(t *testing.T) {
				page, _, posts, _ := pendingBrowser(t, browser, topic)
				page.MustEval(`() => { const b=document.getElementById('message-body'); b.value='pending'; b.form.requestSubmit(); }`)
				var request pendingRequest
				select {
				case request = <-posts:
				case <-page.GetContext().Done():
					t.Fatal("pending request did not arrive")
				}
				switch state {
				case "failed":
					request.reply <- browserReply{status: 400, body: []byte("refused")}
					page.MustWait(`() => window.answers===1`)
				case "confirmed":
					request.reply <- pendingAnswer(t, kernel.ID{51})
					page.MustWait(`() => window.answers===1`)
				case "removed-in-flight":
					// Exercise the request guard independently of the item's lifetime.
					page.MustEval(`() => {
 const form=document.querySelector('[data-pending-form]');
 window.removedAnswers=0;
 form.addEventListener('htmx:afterRequest',()=>window.removedAnswers++,{once:true});
 document.querySelector('[data-pending-remove]').click();
}`)
				}

				if tc.name == "trimmed-limit" {
					// This valid boundary body now queues, including behind a
					// detached form whose original request is still in flight.
					page.MustEval(`text => { const b=document.getElementById('message-body'); b.value=text; b.form.requestSubmit(); }`, tc.text)
					count, requests := 2, 2
					held := state == "sending" || state == "removed-in-flight"
					if held {
						requests = 1
					}
					if state == "removed-in-flight" {
						count = 1
					}
					if page.MustEval(`([count,requests]) => document.querySelectorAll('#message-pending > li').length!==count || window.requests!==requests || window.ordinaryRequests!==0 || document.getElementById('message-body').value!==''`, []int{count, requests}).Bool() {
						t.Fatal("valid boundary send did not queue separately and serialize")
					}
					if held {
						if len(posts) != 0 {
							t.Fatal("queued send overlapped the earlier request")
						}
						request.reply <- browserReply{status: 400, body: []byte("refused")}
					}
					next := receivePending(t, page, posts)
					if strings.TrimSpace(next.values.Get("body")) != strings.Repeat("🙂", 4000) {
						t.Fatal("queued boundary body changed")
					}
					next.reply <- browserReply{status: 400, body: []byte("refused")}
					page.MustWait(`() => !document.querySelector('[data-pending-form].htmx-request')`)
					return
				}
				page.MustEval(`text => { const b=document.getElementById('message-body'); b.value=text; window.draft=b.value; b.form.requestSubmit(); }`, tc.text)
				if problem := page.MustEval(`removed => {
 const b=document.getElementById('message-body'), alert=document.getElementById('message-send-alert');
 if(window.ordinaryRequests || window.requests!==1) return 'busy send started a request';
 if(alert.textContent!==alert.dataset.busy) return 'missing busy notice';
 if(b.value!==window.draft) return 'busy send changed the draft';
 if(!removed && document.querySelector('[data-pending-body]').textContent!=='pending') return 'busy send changed the pending text';
 return '';
}`, state == "removed-in-flight").Str(); problem != "" {
					t.Fatal(problem)
				}
				if len(posts) != 0 {
					t.Fatal("busy send reached the server")
				}
				if state == "sending" || state == "removed-in-flight" {
					request.reply <- browserReply{status: 400, body: []byte("refused")}
					if state == "removed-in-flight" {
						page.MustWait(`() => window.removedAnswers===1`)
						page.MustEval(`() => { const b=document.getElementById('message-body'); b.value='after removal'; b.form.requestSubmit(); }`)
						if page.MustEval(`() => window.requests!==2 || !document.querySelector('#message-pending > li') || document.getElementById('message-body').value!==''`).Bool() {
							t.Fatal("completed detached request did not release a new pending send")
						}
						select {
						case next := <-posts:
							if next.values.Get("body") != "after removal" {
								t.Fatal("send after detached completion changed text")
							}
							next.reply <- browserReply{status: 400, body: []byte("refused")}
						case <-page.GetContext().Done():
							t.Fatal("send after detached completion did not reach the server")
						}
						page.MustWait(`() => !document.querySelector('[data-pending-form].htmx-request')`)
					} else {
						page.MustWait(`() => window.answers===1`)
					}
				}
			})
		}
	}
	for _, tc := range []struct{ name, text, want string }{
		{"trailing-space", strings.Repeat("🙂", 4000) + " ", strings.Repeat("🙂", 4000)},
		{"crlf", strings.Repeat("x\r\n", 1999) + "xx \r\n", strings.Repeat("x\n", 1999) + "xx"},
		{"cr", strings.Repeat("x\r", 1999) + "xx \r", strings.Repeat("x\n", 1999) + "xx"},
	} {
		t.Run("normalized-limit/"+tc.name, func(t *testing.T) {
			page, _, posts, _ := pendingBrowser(t, browser, topic)
			page.MustEval(`text => { const b=document.getElementById('message-body'); b.value=text; b.form.requestSubmit(); }`, tc.text)
			select {
			case request := <-posts:
				if page.MustEval(`() => window.requests!==1 || window.ordinaryRequests!==0`).Bool() || !page.MustHas("#message-pending > li") {
					t.Fatal("valid normalized body bypassed the pending path")
				}
				if page.MustElement("[data-pending-body]").MustText() != tc.want || page.MustElement("#message-body").MustProperty("value").Str() != "" {
					t.Fatal("normalized pending text or cleared composer differs")
				}
				if request.values.Get("body") != strings.ReplaceAll(strings.ReplaceAll(tc.text, "\r\n", "\n"), "\r", "\n") {
					t.Fatal("pending request lost the original textarea text")
				}
				request.reply <- browserReply{status: 400, body: []byte("refused")}
				page.MustWait(`() => window.answers===1`)
			case <-page.GetContext().Done():
				t.Fatal("normalized pending request did not arrive")
			}
		})
	}
	for _, tc := range []struct{ name, text string }{
		{"blank", ""}, {"oversized", strings.Repeat("x", 4001)},
	} {
		t.Run("pending-while-ordinary/"+tc.name, func(t *testing.T) {
			page, _, posts, _ := pendingBrowser(t, browser, topic)
			page.MustEval(`text => { const b=document.getElementById('message-body'); b.value=text; b.form.requestSubmit(); }`, tc.text)
			var ordinary pendingRequest
			select {
			case ordinary = <-posts:
			case <-page.GetContext().Done():
				t.Fatal("ordinary request did not arrive")
			}
			page.MustEval(`() => { const b=document.getElementById('message-body'); b.value='next draft'; b.form.requestSubmit(); }`)
			if problem := page.MustEval(`() => {
 const b=document.getElementById('message-body'), alert=document.getElementById('message-send-alert');
 if(window.ordinaryRequests!==1 || window.requests || document.querySelector('#message-pending > li')) return 'ordinary request overlapped a pending send';
 if(alert.textContent!==alert.dataset.busy) return 'missing busy notice';
 if(b.value!=='next draft') return 'busy send changed the draft';
 return '';
}`).Str(); problem != "" {
				t.Fatal(problem)
			}
			if len(posts) != 0 {
				t.Fatal("busy send reached the server")
			}
			ordinary.reply <- browserReply{status: 400, body: []byte("refused")}
			page.MustWait(`() => !document.querySelector('#message-composer.htmx-request')`)
			page.MustEval(`() => document.getElementById('message-composer').requestSubmit()`)
			select {
			case pending := <-posts:
				if page.MustEval(`() => window.requests!==1 || window.ordinaryRequests!==1`).Bool() || pending.values.Get("body") != "next draft" {
					t.Fatal("completed ordinary request did not release the pending send")
				}
				pending.reply <- browserReply{status: 400, body: []byte("refused")}
				page.MustWait(`() => window.answers===1`)
			case <-page.GetContext().Done():
				t.Fatal("pending request stayed blocked after the ordinary response")
			}
		})
	}
	t.Run("422-then-pending-200", func(t *testing.T) {
		page, _, posts, _ := pendingBrowser(t, browser, topic)
		page.MustEval(`() => { window.help=document.getElementById('message-help').textContent; document.getElementById('message-composer').requestSubmit(); }`)
		var ordinary pendingRequest
		select {
		case ordinary = <-posts:
		case <-page.GetContext().Done():
			t.Fatal("ordinary blank request did not arrive")
		}
		ordinary.reply <- browserReply{status: 422, body: []byte(renderIn(t, "en", view.MessageComposer(view.ChannelPage{BodyError: "message.error.body", Composer: new("generation"), IdempotencyKey: new(ordinary.values.Get("idempotency_key")), EventCursor: new(int64(42))})))}
		page.MustWait(`() => document.getElementById('message-body').getAttribute('aria-invalid')==='true' && !document.querySelector('#message-composer.htmx-request')`)
		if !page.MustHas(`#message-body[aria-describedby="message-help composer-quote"]`) {
			t.Fatal("422 did not retain the error description")
		}
		page.MustEval(`() => { const b=document.getElementById('message-body'); b.value='valid'; b.form.requestSubmit(); }`)
		var pending pendingRequest
		select {
		case pending = <-posts:
		case <-page.GetContext().Done():
			t.Fatal("pending request did not arrive")
		}
		pending.reply <- pendingAnswer(t, kernel.ID{51})
		page.MustWait(`() => window.answers===1`)
		if problem := page.MustEval(`() => {
 const b=document.getElementById('message-body'), f=b.form, typing=document.getElementById('typing-form');
 if(b.getAttribute('aria-invalid')!=='false' || b.getAttribute('aria-describedby')!=='message-help composer-quote') return 'pending success lost field accessibility state';
 if(document.querySelector('#message-composer-state[data-invalid], #message-help[role=alert]') || document.getElementById('message-help')?.textContent!==window.help) return 'pending success retained stale field errors';
 if(f.elements.composer.value!=='fresh' || typing.elements.composer.value!=='fresh' || f.elements.idempotency_key.value!=='00000000-0000-4000-8000-000000000002') return 'pending success did not renew state';
 return '';
}`).Str(); problem != "" {
			t.Fatal(problem)
		}
	})
	t.Run("pending-200-then-422", func(t *testing.T) {
		page, events, posts, _ := pendingBrowser(t, browser, topic)
		page.MustEval(`() => { const b=document.getElementById('message-body'); window.help=document.getElementById('message-help').textContent; b.value='valid'; b.form.requestSubmit(); }`)
		var pending pendingRequest
		select {
		case pending = <-posts:
		case <-page.GetContext().Done():
			t.Fatal("pending request did not arrive")
		}
		pending.reply <- pendingAnswer(t, kernel.ID{51})
		page.MustWait(`() => window.answers===1`)
		if problem := page.MustEval(`() => {
 const b=document.getElementById('message-body');
 if(b.getAttribute('aria-invalid')!=='false' || b.getAttribute('aria-describedby')!=='message-help composer-quote') return 'pending success lost field accessibility state';
 if(document.querySelector('#message-composer-state[data-invalid], #message-help[role=alert]') || document.getElementById('message-help')?.textContent!==window.help) return 'pending success did not restore ordinary help';
 return '';
}`).Str(); problem != "" {
			t.Fatal(problem)
		}
		own := streamTestMessages(kernel.ID{2}, 51)[0]
		events <- realtime.Outgoing{ID: 51, Name: "message", Data: []byte(renderIn(t, "en", view.LiveMessageItem(own)))}
		page.MustWait(`() => !document.querySelector('#message-pending > li')`)
		page.MustEval(`() => document.getElementById('message-composer').requestSubmit()`)
		var ordinary pendingRequest
		select {
		case ordinary = <-posts:
		case <-page.GetContext().Done():
			t.Fatal("ordinary blank request did not arrive")
		}
		if ordinary.values.Get("body") != "" || page.MustEval(`() => window.ordinaryRequests!==1 || window.requests!==1`).Bool() {
			t.Fatal("blank submit did not take the ordinary path")
		}
		ordinary.reply <- browserReply{status: 422, body: []byte(renderIn(t, "en", view.MessageComposer(view.ChannelPage{BodyError: "message.error.body", Composer: new("fresh"), IdempotencyKey: new(ordinary.values.Get("idempotency_key")), EventCursor: new(int64(51))})))}
		page.MustWait(`() => !document.querySelector('#message-composer.htmx-request')`)
		if problem := page.MustEval(`() => {
 const b=document.getElementById('message-body'), help=document.getElementById('message-help');
 if(b.getAttribute('aria-invalid')!=='true' || b.getAttribute('aria-describedby')!=='message-help composer-quote') return '422 lost the error description after pending success';
 if(!document.querySelector('#message-composer-state[data-invalid]') || help?.getAttribute('role')!=='alert' || help.textContent!=='Enter 1–4000 characters without control characters or directional embeddings and overrides.') return '422 did not render the field error';
 return '';
}`).Str(); problem != "" {
			t.Fatal(problem)
		}
	})
	for _, width := range []int{1280, 390} {
		for _, first := range []string{"response", "stream"} {
			for _, space := range []string{"\u3000", "\ufeff"} {
				t.Run(fmt.Sprintf("%d/%s/%x", width, first, space), func(t *testing.T) {
					page, events, posts, reads := pendingBrowser(t, browser, topic)
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
   const b=document.getElementById('message-body'), p=document.querySelector('[data-pending-body]'), s=document.getElementById('organization-stream');
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
						page.MustEval(`text => { const b=document.getElementById('message-body'); b.value=text; b.form.requestSubmit(); }`, " ")
						if page.MustEval(`() => window.requests`).Int() != 1 || len(posts) != 0 || page.MustElement("#message-body").MustProperty("value").Str() != " " || page.MustElement("#message-send-alert").MustText() == "" {
							t.Fatal("busy send lost text or started a request")
						}
						page.MustEval(`text => document.getElementById("message-body").value=text`, draft)
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
					page.MustEval(`() => { const r=document.querySelector('[data-pending-body]').getBoundingClientRect(); window.position=[r.x,r.y]; }`)
					out := realtime.Outgoing{ID: 51, Name: "message", Data: []byte(renderIn(t, "en", view.LiveMessageItem(own)))}
					events <- out
					page.MustWait(`() => !!document.querySelector('#message-items > #message-33000000000000000000000000000000')`)
					if !topic {
						moved := own
						moved.TopicID = kernel.ID{4}
						events <- realtime.Outgoing{ID: 52, Name: "messages-moved", Data: []byte(renderIn(t, "en", view.MovedMessageItems([]view.Message{moved}, kernel.ID{2}, kernel.ID{4})))}
						page.MustWait(`() => document.getElementById('organization-stream').dataset.eventCursor==='52'`)
					}
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
	for _, topic := range []bool{false, true} {
		t.Run(fmt.Sprintf("topic=%t", topic), func(t *testing.T) { testPendingFailurePage(t, browser, topic) })
	}
}

func testPendingFailurePage(t *testing.T, browser *rod.Browser, topic bool) {
	page, _, posts, _ := pendingBrowser(t, browser, topic)
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
		page.MustEval(`() => { const b=document.getElementById("message-body"); b.value=" "; b.form.requestSubmit(); }`)
		if page.MustEval(`() => document.getElementById("message-body").value!==" "`).Bool() || len(sent) != 0 {
			t.Fatal("failed item did not block an ordinary submit")
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

// Gate answers and stream application independently to isolate move-out rules.
func testPendingMoveBrowser(t *testing.T, browser *rod.Browser) {
	t.Run("nonfirst-confirmed-frees-capacity", func(t *testing.T) {
		page, events, posts, _ := pendingBrowser(t, browser, true)
		page.MustEval(`() => {
 const b=document.getElementById('message-body');
 for(let i=0;i<10;i++) {b.value='pending '+i;b.form.requestSubmit();}
 window.nodes=[...document.querySelectorAll('#message-pending > li')];
}`)
		for i := 0; i < 2; i++ {
			request := receivePending(t, page, posts)
			request.reply <- pendingAnswer(t, kernel.ID{byte(51 + i)})
			page.MustWait(`n => window.answers===n`, i+1)
		}
		third := receivePending(t, page, posts)
		if page.MustEval(`() => window.nodes.length!==10 || window.nodes.slice(0,2).some(n=>!n.dataset.postedMessage)`).Bool() {
			t.Fatal("queue did not retain two confirmed items at capacity")
		}
		page.MustEval(`() => {const b=document.getElementById('message-body');b.value='freed slot';b.form.requestSubmit();}`)
		if page.MustEval(`() => {
 const a=document.getElementById('message-send-alert');
 return !a.textContent || a.textContent!==a.dataset.capacity || document.getElementById('message-body').value!=='freed slot';
}`).Bool() {
			t.Fatal("full queue did not retain the draft with a capacity notice")
		}
		move := func(id byte, seq int64) {
			t.Helper()
			moved := streamTestMessages(kernel.ID{2}, int(id))[0]
			moved.TopicID = kernel.ID{4}
			events <- realtime.Outgoing{ID: seq, Name: "messages-moved", Data: []byte(renderIn(t, "en", view.MovedMessageItems([]view.Message{moved}, kernel.ID{2}, moved.TopicID)))}
			page.MustWait(`seq => document.getElementById('organization-stream').dataset.appliedCursor===String(seq)`, seq)
		}
		move(50, 53) // An unrelated move from this topic must preserve the notice.
		if page.MustEval(`() => {
 const a=document.getElementById('message-send-alert');
 return a.textContent!==a.dataset.capacity || window.nodes.some(n=>!n.isConnected);
}`).Bool() {
			t.Fatal("unrelated move cleared the capacity notice or removed a pending item")
		}
		move(52, 54)
		if page.MustEval(`() => {
 const nodes=[...document.querySelectorAll('#message-pending > li')];
 return nodes.length!==9 || window.nodes[1].isConnected ||
  nodes.some((n,i)=>n!==window.nodes[i<1?i:i+1]);
}`).Bool() {
			t.Fatal("move must remove only the second confirmed item and keep the first")
		}
		page.MustEval(`() => document.getElementById('message-body').form.requestSubmit()`)
		if page.MustEval(`() => {
 const nodes=[...document.querySelectorAll('#message-pending > li')], last=nodes.at(-1);
 return nodes.length!==10 || document.getElementById('message-body').value!=='' ||
  document.getElementById('message-send-alert').textContent!=='' ||
  last.querySelector('[data-pending-body]').textContent!=='freed slot' ||
  last.querySelector('form').dataset.state!=='queued' || window.requests!==3 || window.ordinaryRequests!==0;
}`).Bool() || len(posts) != 0 {
			t.Fatal("move did not free a slot for the next queued send without a capacity notice")
		}
		third.reply <- browserReply{status: 500, body: []byte("pause")}
		page.MustWait(`() => window.answers===3`)
	})
	for _, order := range []string{"post-move-answer", "answer-move", "lost-move-retry", "lost-retry-post-move", "move-answer"} {
		t.Run(order, func(t *testing.T) {
			page, events, posts, _ := pendingBrowser(t, browser, true)
			earlyRetry := order == "lost-retry-post-move"
			if order == "lost-move-retry" || earlyRetry {
				// Closing a reused server connection before headers can make Chrome
				// resend transparently, leaving answers at zero with another held POST.
				// Lose the rendered answer at the browser boundary, as in the retry tests.
				var answered atomic.Bool
				router := page.HijackRequests()
				router.MustAdd("*/organizations/acme/channels/*", func(h *rod.Hijack) {
					if h.Request.Method() != "POST" || strings.HasSuffix(h.Request.URL().Path, "/read") || strings.HasSuffix(h.Request.URL().Path, "/typing") || answered.Swap(true) {
						h.ContinueRequest(&proto.FetchContinueRequest{})
						return
					}
					if err := h.LoadResponse(http.DefaultClient, true); err != nil {
						t.Error(err)
					}
					h.Response.Fail(proto.NetworkErrorReasonConnectionReset)
				})
				go router.Run()
				t.Cleanup(func() { _ = router.Stop() })
			}
			own := streamTestMessages(kernel.ID{2}, 51)[0]
			own.Body, own.Text = "moving post", "moving post"
			answer := pendingAnswer(t, own.ID) // Rendered before any move, still naming A.
			page.MustEval(`earlyRetry => {
    const b=document.getElementById('message-body'); b.value='moving post'; b.form.requestSubmit();
    window.frames=[]; window.badTasks=[];
    const count=()=>document.querySelectorAll('#message-items > #message-33000000000000000000000000000000').length+
      [...document.querySelectorAll('[data-pending-body]')].filter(e=>e.textContent==='moving post').length;
    const check=()=>{
      const moved=document.getElementById('organization-stream').dataset.appliedCursor==='52';
      const confirmed=!!document.querySelector('#message-composer-state [name=composer][value=fresh]');
      const n=count();
      if(moved && confirmed && n!==0) window.badTasks.push(n);
      return [n,moved,confirmed,!!document.querySelector('#message-items > #message-33000000000000000000000000000000')];
    };
    document.addEventListener('htmx:afterSwap',e=>queueMicrotask(()=>{
      check();
      if(earlyRetry && e.target.id==='message-composer-state') {
        const s=document.getElementById('organization-stream');
        window.retryTask=[!!document.querySelector('#message-pending > li'),s.dataset.appliedCursor,s.dataset.eventCursor];
      }
    }));
    document.addEventListener('htmx:sseBeforeMessage',()=>queueMicrotask(check));
    const frame=()=>{ window.frames.push(check()); window.frame=requestAnimationFrame(frame); }; frame();
   }`, earlyRetry)
			receive := func() pendingRequest {
				t.Helper()
				select {
				case request := <-posts:
					return request
				case <-page.GetContext().Done():
					t.Fatal("pending request did not arrive")
					return pendingRequest{}
				}
			}
			request := receive()
			if !page.MustEval(`() => document.querySelector('[data-pending-form]').getAttribute('action')===document.getElementById('message-composer').getAttribute('action') && document.querySelector('[data-pending-form]').action.includes('/topics/')`).Bool() {
				t.Fatal("pending form did not post to the topic")
			}
			switch order {
			case "answer-move":
				request.reply <- answer
				page.MustWait(`() => window.answers===1`)
			case "lost-move-retry", "lost-retry-post-move":
				request.reply <- answer
				page.MustWait(`() => window.answers===1`)
			}
			moved := own
			moved.TopicID = kernel.ID{4}
			var next pendingRequest
			if earlyRetry {
				page.MustElement("[data-pending-actions] button[type=submit]").MustClick()
				retry := receive()
				if retry.values.Encode() != request.values.Encode() {
					t.Fatal("retry changed form values")
				}
				retry.reply <- pendingTopicAnswer(t, own.ID, moved.TopicID)
				page.MustWait(`() => window.answers===2`)
				if problem := page.MustEval(`() => {
    if(!window.retryTask || window.retryTask[0]) return 'routing answer kept pending in its task';
    if(window.retryTask[1] || window.retryTask[2]!=='42') return 'stream reached page before routing answer';
    if(document.querySelector('#message-pending > li')) return 'routing answer kept pending';
    const b=document.getElementById('message-body'); b.value='next'; b.form.requestSubmit();
    if(window.requests!==3) return 'routing answer blocked next request';
    return '';
   }`).Str(); problem != "" {
					t.Fatal(problem)
				}
				next = receive()
				if next.values.Get("body") != "next" {
					t.Fatal("send after routing answer changed text")
				}
			}
			if order == "post-move-answer" || earlyRetry {
				events <- realtime.Outgoing{ID: 51, Name: "message", Data: []byte(renderIn(t, "en", view.LiveMessageItem(own)))}
				page.MustWait(`() => !!document.querySelector('#message-items > #message-33000000000000000000000000000000')`)
				if earlyRetry {
					page.MustEval(`() => new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))`)
				}
			}
			events <- realtime.Outgoing{ID: 52, Name: "messages-moved", Data: []byte(renderIn(t, "en", view.MovedMessageItems([]view.Message{moved}, own.TopicID, moved.TopicID)))}
			page.MustWait(`() => document.getElementById('organization-stream').dataset.appliedCursor==='52'`)
			if order != "answer-move" && !earlyRetry {
				if !page.MustHas("#message-pending > li") || page.MustHas("#message-items > li") {
					t.Fatal("move before confirmation must leave only the pending item")
				}
				if order == "lost-move-retry" {
					page.MustElement("[data-pending-actions] button[type=submit]").MustClick()
					retry := receive()
					if retry.values.Encode() != request.values.Encode() {
						t.Fatal("retry changed form values")
					}
					retry.reply <- pendingTopicAnswer(t, own.ID, moved.TopicID)
				} else {
					request.reply <- answer
				}
			}
			if !earlyRetry {
				page.MustWait(`() => !document.querySelector('#message-pending > li')`)
			}
			page.MustEval(`() => new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))`)
			if problem := page.MustEval(`earlyRetry => {
    cancelAnimationFrame(window.frame);
    if(window.badTasks.length) return 'move-out not removed in same task: '+window.badTasks;
    if(window.frames.some(([n,moved,confirmed,real])=> n!==(earlyRetry&&confirmed ? (real?1:0) : (moved ? (confirmed?0:1) : (real&&!confirmed?2:1))))) return 'frame identities: '+JSON.stringify(window.frames);
    if(earlyRetry && !window.frames.some(([n,moved,confirmed,real])=>n===1&&!moved&&confirmed&&real)) return 'missing real post frame';
    if((!earlyRetry && document.querySelector('#message-pending > li')) ||
       document.querySelector('#message-items > #message-33000000000000000000000000000000') ||
       [...document.querySelectorAll('[data-pending-body]')].some(e=>e.textContent==='moving post')) return 'identity remained';
    if(earlyRetry) return '';
    const b=document.getElementById('message-body'); b.value='next'; b.form.requestSubmit(); return '';
   }`, earlyRetry).Str(); problem != "" {
				t.Fatal(problem)
			}
			if !earlyRetry {
				next = receive()
			}
			next.reply <- pendingAnswer(t, kernel.ID{60})
		})
	}
}

// Queue scenarios keep each POST held until its order and pending state are checked.
func testPendingQueueBrowser(t *testing.T, browser *rod.Browser) {
	for _, topic := range []bool{false, true} {
		t.Run(fmt.Sprintf("topic=%t", topic), func(t *testing.T) {
			t.Run("capacity-all-states", func(t *testing.T) {
				page, events, posts, _ := pendingBrowser(t, browser, topic)
				send := func(text string) {
					page.MustEval(`text => {const b=document.getElementById('message-body'); b.value=text; b.form.requestSubmit();}`, text)
				}
				answer := func(status int, id byte, n int) {
					request := receivePending(t, page, posts)
					reply := pendingAnswer(t, kernel.ID{id})
					if status != 200 {
						reply = browserReply{status: status, body: []byte("refused")}
					}
					request.reply <- reply
					page.MustWait(`n => window.answers===n`, n)
				}
				send("refused")
				answer(422, 50, 1)
				send("confirmed")
				answer(200, 51, 2)
				send("unknown")
				answer(500, 52, 3)
				for i := 3; i < 10; i++ {
					send(fmt.Sprint(i))
				}
				reject := func() {
					t.Helper()
					send("eleventh")
					if page.MustEval(`() => {
      const b=document.getElementById('message-body'), a=document.getElementById('message-send-alert');
      const bodies=[...document.querySelectorAll('[data-pending-body]')];
      return b.value!=='eleventh' || a.textContent!==a.dataset.capacity || !a.textContent ||
       b.getAttribute('aria-invalid')!=='false' || b.getAttribute('aria-describedby')!=='message-help composer-quote' ||
       bodies.length!==10 || new Set(bodies.map(b=>b.id)).size!==10 ||
       [...document.querySelectorAll('[data-pending-form] button')].some(b=>!document.getElementById(b.getAttribute('aria-describedby')));
     }`).Bool() {
						t.Fatal("capacity did not retain draft, count every state, or preserve accessibility")
					}
				}
				reject() // Includes an unknown outcome, queued, refused and confirmed items.
				if page.MustEval(`() => window.requests!==3 || window.ordinaryRequests!==0`).Bool() || len(posts) != 0 {
					t.Fatal("unknown outcome did not pause queued sends")
				}
				page.MustEval(`() => document.querySelectorAll('[data-pending-form]')[2].requestSubmit()`)
				retry := receivePending(t, page, posts)
				reject() // The same ten slots now include an in-flight retry.
				if page.MustEval(`() => window.requests!==4`).Bool() {
					t.Fatal("retry at capacity did not use its existing slot")
				}
				retry.reply <- pendingAnswer(t, kernel.ID{52})
				next := receivePending(t, page, posts) // FIFO resumes at the first queued item.
				if next.values.Get("body") != "3" {
					t.Fatal("retry did not resume the FIFO head")
				}
				own := streamTestMessages(kernel.ID{2}, 51)[0]
				events <- realtime.Outgoing{ID: 51, Name: "message", Data: []byte(renderIn(t, "en", view.LiveMessageItem(own)))}
				page.MustWait(`() => document.querySelectorAll('#message-pending > li').length===9`)
				send("delivery freed slot")
				if page.MustElement("#message-body").MustProperty("value").Str() != "" {
					t.Fatal("delivery did not free capacity")
				}
				page.MustElement("[data-pending-remove]").MustClick()
				send("remove freed slot")
				if page.MustEval(`() => document.getElementById('message-body').value!=='' || document.querySelectorAll('#message-pending > li').length!==10 || window.requests!==5 || window.ordinaryRequests!==0`).Bool() || len(posts) != 0 {
					t.Fatal("Remove did not free capacity or queue overlapped an in-flight send")
				}
				next.reply <- browserReply{status: 500, body: []byte("pause")}
			})
			for _, mode := range []string{"not-sent-retry-last", "unknown-committed", "unknown-not-committed", "unknown-remove", "identical-delayed", "identical-lost", "identical-replay"} {
				t.Run(mode, func(t *testing.T) {
					page, events, posts, _ := pendingBrowser(t, browser, topic)
					if mode == "identical-lost" {
						var lost atomic.Bool
						router := page.HijackRequests()
						router.MustAdd("*/organizations/acme/channels/*", func(h *rod.Hijack) {
							if h.Request.Method() != "POST" || strings.HasSuffix(h.Request.URL().Path, "/read") || strings.HasSuffix(h.Request.URL().Path, "/typing") || lost.Swap(true) {
								h.ContinueRequest(&proto.FetchContinueRequest{})
								return
							}
							if err := h.LoadResponse(http.DefaultClient, true); err != nil {
								t.Error(err)
							}
							h.Response.Fail(proto.NetworkErrorReasonConnectionReset)
						})
						go router.Run()
						t.Cleanup(func() { _ = router.Stop() })
					}
					identical := strings.HasPrefix(mode, "identical-")
					bodies := []string{"first", "second"}
					if identical {
						bodies = []string{"A", "A"}
					}
					page.MustEval(`bodies => {
      const b=document.getElementById('message-body');
      for(const text of bodies) {b.value=text;b.form.requestSubmit();}
      window.nodes=[...document.querySelectorAll('#message-pending > li')];
      window.badFrames=[];
      const frame=()=>{
       window.nodes.forEach((node,i)=>{
        const id=['message-33000000000000000000000000000000','message-34000000000000000000000000000000'][i];
        const real=!!document.getElementById(id), confirmed=!!node.dataset.postedMessage;
        const n=Number(real)+Number(node.isConnected);
        if(n!==(real&&!confirmed?2:1)) window.badFrames.push([i,n,real,confirmed]);
       });
       window.frame=requestAnimationFrame(frame);
      }; frame();
     }`, bodies)
					first := receivePending(t, page, posts)
					if page.MustEval(`() => window.requests!==1 || window.ordinaryRequests || window.nodes.length!==2 || document.getElementById('message-body').value!==''`).Bool() || len(posts) != 0 {
						t.Fatal("quick sends did not queue behind one request")
					}
					deliver := func(id byte) {
						own := streamTestMessages(kernel.ID{2}, int(id))[0]
						own.Body = bodies[int(id)-51]
						own.Text = own.Body
						if mode == "unknown-remove" && id == 51 {
							own.EventSeq = 53
						}
						events <- realtime.Outgoing{ID: own.EventSeq, Name: "message", Data: []byte(renderIn(t, "en", view.LiveMessageItem(own)))}
						page.MustWait(`id => !!document.getElementById(id)`, view.MessageDOMID(own.ID))
					}
					unknown := strings.HasPrefix(mode, "unknown-") || mode == "identical-lost"
					status := 200
					if mode == "not-sent-retry-last" {
						status = 422
					}
					if strings.HasPrefix(mode, "unknown-") {
						status = 500
					}
					reply := pendingAnswer(t, kernel.ID{51})
					if status != 200 {
						reply = browserReply{status: status, body: []byte("refused")}
					}
					first.reply <- reply
					page.MustWait(`() => window.answers===1`)
					if unknown {
						if page.MustEval(`() => window.requests!==1 || document.querySelectorAll('[data-state=queued]').length!==1`).Bool() || len(posts) != 0 {
							t.Fatal("unknown outcome dispatched a later send")
						}
						if mode == "unknown-committed" || mode == "identical-lost" {
							deliver(51)
						}
						if mode == "unknown-remove" {
							page.MustElement("[data-pending-remove]").MustClick()
						} else {
							page.MustEval(`() => window.nodes[0].querySelector('form').requestSubmit()`)
							retry := receivePending(t, page, posts)
							if retry.values.Encode() != first.values.Encode() {
								t.Fatal("unknown retry changed key, body or place")
							}
							if page.MustEval(`() => window.requests!==2`).Bool() || len(posts) != 0 {
								t.Fatal("retry overlapped the later send")
							}
							retry.reply <- pendingAnswer(t, kernel.ID{51})
						}
					}
					second := receivePending(t, page, posts)
					if second.values.Get("body") != bodies[1] || first.values.Get("idempotency_key") == second.values.Get("idempotency_key") {
						t.Fatal("FIFO body or independent key differs")
					}
					if mode == "not-sent-retry-last" {
						page.MustElement("[data-pending-actions] button[type=submit]").MustClick()
						if page.MustEval(`() => document.querySelector('#message-pending > li')!==window.nodes[1] || window.requests!==2 || document.activeElement!==document.getElementById('message-body')`).Bool() || len(posts) != 0 {
							t.Fatal("Not sent retry did not move to the list/dispatch tail")
						}
					}
					if mode == "identical-replay" {
						deliver(51) // A's ID cannot provisionally match the still-unconfirmed A′.
						if !page.MustEval(`() => window.nodes[1].isConnected`).Bool() {
							t.Fatal("another message ID matched A′")
						}
						page.MustEval(`() => {window.source.close(); window.source.dispatchEvent(new Event('error'));}`)
						page.MustWait(`() => window.opens===2`)
					}
					second.reply <- pendingAnswer(t, kernel.ID{52})
					answers := 2
					if unknown && mode != "unknown-remove" {
						answers++
					}
					page.MustWait(`n => window.answers===n`, answers)
					if mode == "not-sent-retry-last" {
						retry := receivePending(t, page, posts)
						if retry.values.Encode() != first.values.Encode() {
							t.Fatal("Not sent retry changed the original send")
						}
						retry.reply <- pendingAnswer(t, kernel.ID{53})
						page.MustWait(`() => window.answers===3`)
						return
					}
					if mode == "unknown-remove" {
						deliver(52)
						deliver(51) // The removed original commits after its successor.
					} else {
						deliver(51)
						deliver(52)
					}
					if mode == "identical-replay" {
						deliver(51)
						deliver(52)
					}
					page.MustWait(`() => !document.querySelector('#message-pending > li')`)
					page.MustEval(`() => new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))`)
					if page.MustEval(`removed => {cancelAnimationFrame(window.frame);return (!removed && window.badFrames.length>0) || document.querySelectorAll('#message-items > li').length!==2;}`, mode == "unknown-remove").Bool() {
						t.Fatal("per-message frame identity or late original delivery differs")
					}
				})
			}
		})
	}
}

func receivePending(t *testing.T, page *rod.Page, posts <-chan pendingRequest) pendingRequest {
	t.Helper()
	select {
	case request := <-posts:
		return request
	case <-page.GetContext().Done():
		t.Fatal("pending request did not arrive")
		return pendingRequest{}
	}
}

func testPendingEditBrowser(t *testing.T, browser *rod.Browser) {
	t.Run("unconfirmed", func(t *testing.T) { testPendingEditUnconfirmedBrowser(t, browser) })
	for _, mode := range []string{"unused", "equivalent", "normalized", "changed"} {
		t.Run(mode, func(t *testing.T) {
			page, events, posts, _ := pendingBrowser(t, browser, false)
			original, edited, status := "original", "original", 500
			switch mode {
			case "unused":
				original, edited, status = "bad\u0001", "corrected", 422
			case "normalized":
				edited = " \noriginal \n"
			case "changed":
				edited = "changed"
			}
			page.MustEval(`text => {const b=document.getElementById('message-body');b.value=text;b.form.requestSubmit();}`, original)
			first := receivePending(t, page, posts)
			first.reply <- browserReply{status: status, body: []byte("refused")}
			page.MustWait(`() => window.answers===1`)
			page.MustEval(`() => {const b=document.getElementById('message-body');b.value=' ';b.dispatchEvent(new Event('input',{bubbles:true}));document.querySelector('[data-pending-edit]').click();}`)
			if page.MustEval(`() => {const e=document.querySelector('[data-pending-edit]'), h=document.querySelector('[data-pending-edit-help]');return !e.disabled || e.hidden || !e.checkVisibility() || !h.checkVisibility() || !e.getAttribute('aria-describedby').split(' ').includes(h.id) || h.textContent!=='Clear the text you are writing to edit this message.' || document.getElementById('message-body').value!==' ';}`).Bool() {
				t.Fatal("occupied composer did not disable and describe Edit")
			}
			page.MustEval(`() => {const b=document.getElementById('message-body');b.value='';b.dispatchEvent(new Event('input',{bubbles:true}));}`)
			page.MustElement("[data-pending-edit]").MustClick()
			if page.MustEval(`([text,key]) => {const b=document.getElementById('message-body');return b.value!==text || b.form.elements.idempotency_key.value!==key || document.activeElement!==b || b.selectionStart!==text.length || b.selectionEnd!==text.length || !!document.querySelector('#message-pending > li');}`, []string{original, first.values.Get("idempotency_key")}).Bool() {
				t.Fatal("Edit did not restore text/key, end caret, focus and remove item")
			}
			page.MustEval(`text => {const b=document.getElementById('message-body');b.value=text;b.dispatchEvent(new Event('input',{bubbles:true}));b.form.requestSubmit();b.value=text+' next draft';b.dispatchEvent(new Event('input',{bubbles:true}));window.draft=b.value;window.state=document.getElementById('message-composer-state');}`, edited)
			resend := receivePending(t, page, posts)
			if resend.values.Get("idempotency_key") != first.values.Get("idempotency_key") || resend.values.Get("body") != edited {
				t.Fatal("edited resend lost text or original key")
			}
			if mode == "changed" {
				// Even a composer-shaped 409 must stay confined to the pending item.
				conflict := pendingAnswer(t, kernel.ID{51})
				conflict.status = 409
				resend.reply <- conflict
				page.MustWait(`() => window.answers===2`)
				if page.MustEval(`key => {const f=document.querySelector('[data-pending-form]');return f.dataset.state!=='failed' || f.elements.idempotency_key.value===key || f.querySelector('[role=status]').textContent!==document.getElementById('message-send-alert').dataset.repeat || document.getElementById('message-composer-state')!==window.state;}`, first.values.Get("idempotency_key")).Bool() {
					t.Fatal("409 did not fail locally with repeat and a fresh key")
				}
				stored := streamTestMessages(kernel.ID{2}, 51)[0]
				stored.Body, stored.Text = original, original
				events <- realtime.Outgoing{ID: 51, Name: "message", Data: []byte(renderIn(t, "en", view.LiveMessageItem(stored)))}
				page.MustWait(`() => document.querySelectorAll('#message-items > li').length===1`)
				fresh := page.MustEval(`() => document.querySelector('[data-pending-form]').elements.idempotency_key.value`).Str()
				page.MustElement("[data-pending-actions] button[type=submit]").MustClick()
				resend = receivePending(t, page, posts)
				if resend.values.Get("idempotency_key") != fresh || resend.values.Get("body") != edited {
					t.Fatal("Retry did not send changed text with the fresh key")
				}
			}
			resend.reply <- pendingAnswer(t, kernel.ID{52})
			answers := 2
			if mode == "changed" {
				answers++
			}
			page.MustWait(`n => window.answers===n`, answers)
			if page.MustEval(`() => {const b=document.getElementById('message-body');return b.value!==window.draft || b.getAttribute('aria-invalid')!=='false' || b.getAttribute('aria-describedby')!=='message-help composer-quote' || document.querySelector('[data-pending-form]').dataset.state!=='confirmed';}`).Bool() {
				t.Fatal("pending answer changed the composer or did not confirm the item")
			}
			if page.MustEval(`key => document.getElementById('message-composer').elements.idempotency_key.value===key`, first.values.Get("idempotency_key")).Bool() {
				t.Fatal("successful resend kept the original key in the composer")
			}
			own := streamTestMessages(kernel.ID{2}, 52)[0]
			own.Body, own.Text = strings.TrimSpace(edited), strings.TrimSpace(edited)
			events <- realtime.Outgoing{ID: 52, Name: "message", Data: []byte(renderIn(t, "en", view.LiveMessageItem(own)))}
			page.MustWait(`() => !document.querySelector('#message-pending > li')`)
			want := 1
			if mode == "changed" {
				want = 2
			}
			if page.MustEval(`n => document.querySelectorAll('#message-items > li').length!==n`, want).Bool() {
				t.Fatal("resend did not leave exactly the expected message identities")
			}
			nextBody := page.MustEval(`() => document.getElementById('message-body').value`).Str()
			page.MustEval(`() => document.getElementById('message-composer').requestSubmit()`)
			next := receivePending(t, page, posts)
			if next.values.Get("body") != nextBody || next.values.Get("idempotency_key") == "" || next.values.Get("idempotency_key") == first.values.Get("idempotency_key") || next.values.Get("idempotency_key") == resend.values.Get("idempotency_key") {
				t.Fatal("following message did not send different text with a new key")
			}
			next.reply <- pendingAnswer(t, kernel.ID{53})
			page.MustWait(`n => window.answers===n`, answers+1)
			if page.MustEval(`() => document.getElementById('message-body').value!=='' || document.querySelector('[data-pending-form]').dataset.state!=='confirmed' || window.ordinaryRequests!==0`).Bool() {
				t.Fatal("following message did not post normally")
			}
			delivered := streamTestMessages(kernel.ID{2}, 53)[0]
			delivered.Body, delivered.Text = strings.TrimSpace(nextBody), strings.TrimSpace(nextBody)
			events <- realtime.Outgoing{ID: 53, Name: "message", Data: []byte(renderIn(t, "en", view.LiveMessageItem(delivered)))}
			page.MustWait(`n => !document.querySelector('#message-pending > li') && document.querySelectorAll('#message-items > li').length===n`, want+1)
		})
	}
	for _, mode := range []string{"draft-click-guard", "composition-availability", "composition-click-guard"} {
		t.Run(mode, func(t *testing.T) {
			page, _, posts, _ := pendingBrowser(t, browser, false)
			page.MustEval(`() => {const b=document.getElementById('message-body');b.value='original';b.form.requestSubmit();}`)
			first := receivePending(t, page, posts)
			first.reply <- browserReply{status: 500, body: []byte("unknown")}
			page.MustWait(`() => window.answers===1`)
			if page.MustEval(`() => document.querySelector('[data-pending-edit]').disabled`).Bool() {
				t.Fatal("Edit was unavailable with an empty composer")
			}
			key := page.MustEval(`() => {window.failedItem=document.querySelector('#message-pending > li');return document.getElementById('message-composer').elements.idempotency_key.value;}`).Str()
			if mode == "composition-availability" {
				page.MustEval(`() => document.getElementById('message-body').dispatchEvent(new CompositionEvent('compositionstart',{bubbles:true}))`)
				if !page.MustEval(`() => document.querySelector('[data-pending-edit]').disabled`).Bool() {
					t.Fatal("Edit stayed enabled during composition")
				}
				page.MustEval(`() => document.getElementById('message-body').dispatchEvent(new CompositionEvent('compositionend',{bubbles:true}))`)
				if page.MustEval(`() => document.querySelector('[data-pending-edit]').disabled`).Bool() {
					t.Fatal("Edit stayed disabled after composition ended with an empty composer")
				}
				return
			}
			want := ""
			if mode == "draft-click-guard" {
				// No input event: leave Edit enabled to exercise the click guard itself.
				want = "new draft"
				page.MustEval(`text => {document.getElementById('message-body').value=text;}`, want)
			} else {
				page.MustEval(`() => {document.getElementById('message-body').dispatchEvent(new CompositionEvent('compositionstart',{bubbles:true}));document.querySelector('[data-pending-edit]').disabled=false;}`)
			}
			if page.MustEval(`() => document.querySelector('[data-pending-edit]').disabled`).Bool() {
				t.Fatal("click-guard test requires an enabled Edit button")
			}
			page.MustEval(`() => document.querySelector('[data-pending-edit]').click()`)
			if page.MustEval(`([text,key]) => {const b=document.getElementById('message-body');return b.value!==text || b.form.elements.idempotency_key.value!==key || document.querySelector('#message-pending > li')!==window.failedItem || window.failedItem.querySelector('form').dataset.state!=='failed';}`, []string{want, key}).Bool() {
				t.Fatal("Edit click changed the draft, key or failed item while unavailable")
			}
		})
	}
	t.Run("edit-resumes-and-frees-slot", func(t *testing.T) {
		page, _, posts, _ := pendingBrowser(t, browser, false)
		page.MustEval(`() => {const b=document.getElementById('message-body');for(let n=0;n<10;n++){b.value='queued '+n;b.form.requestSubmit();}}`)
		first := receivePending(t, page, posts)
		first.reply <- browserReply{status: 500, body: []byte("unknown")}
		page.MustWait(`() => window.answers===1`)
		page.MustElement("[data-pending-edit]").MustClick()
		next := receivePending(t, page, posts)
		if next.values.Get("body") != "queued 1" || page.MustEval(`() => document.querySelectorAll('#message-pending > li').length!==9 || window.requests!==2 || window.ordinaryRequests!==0`).Bool() {
			t.Fatal("Edit did not free capacity and resume the paused FIFO")
		}
		next.reply <- pendingAnswer(t, kernel.ID{51})
		third := receivePending(t, page, posts)
		if page.MustEval(`key => {const b=document.getElementById('message-body');return b.value!=='queued 0' || b.form.elements.idempotency_key.value!==key || b.getAttribute('aria-invalid')!=='false' || b.getAttribute('aria-describedby')!=='message-help composer-quote';}`, first.values.Get("idempotency_key")).Bool() {
			t.Fatal("another send's state swap replaced the restored key or text")
		}
		page.MustEval(`() => document.getElementById('message-composer').requestSubmit()`)
		if page.MustEval(`key => document.getElementById('message-body').value!=='' || document.querySelectorAll('#message-pending > li').length!==10 || document.querySelector('#message-pending > li:last-child form').elements.idempotency_key.value!==key || window.requests!==3 || window.ordinaryRequests!==0`, first.values.Get("idempotency_key")).Bool() || len(posts) != 0 {
			t.Fatal("restored send could not use the freed slot or overlapped a send")
		}
		third.reply <- browserReply{status: 500, body: []byte("pause")}
		page.MustWait(`() => window.answers===3`)
		page.MustElement("[data-state=failed] [data-pending-edit]").MustClick()
		key := page.MustEval(`() => document.getElementById('message-composer').elements.idempotency_key.value`).Str()
		page.MustEval(`() => {const b=document.getElementById('message-body');b.value='';b.dispatchEvent(new Event('input',{bubbles:true}));window.emptyKey=b.form.elements.idempotency_key.value;b.value='replacement';b.dispatchEvent(new Event('input',{bubbles:true}));b.form.requestSubmit();}`)
		if page.MustEval(`key => window.emptyKey===key || document.querySelector('#message-pending > li:last-child form').elements.idempotency_key.value===key`, key).Bool() {
			t.Fatal("emptying edited text did not discard its original key")
		}
		fourth := receivePending(t, page, posts)
		fourth.reply <- browserReply{status: 500, body: []byte("pause")}
	})
}

func testPendingEditUnconfirmedBrowser(t *testing.T, browser *rod.Browser) {
	restore := func(t *testing.T) (*rod.Page, chan pendingRequest, string) {
		t.Helper()
		page, _, posts, _ := pendingBrowser(t, browser, false)
		page.MustEval(`() => {const b=document.getElementById('message-body');b.value='hello';b.form.requestSubmit();}`)
		first := receivePending(t, page, posts)
		// A 5xx may follow a commit, so the outcome stays unknown without delivery.
		first.reply <- browserReply{status: 500, body: []byte("unknown")}
		page.MustWait(`() => window.answers===1`)
		page.MustElement("[data-pending-edit]").MustClick()
		return page, posts, first.values.Get("idempotency_key")
	}
	for _, status := range []int{429, 422} {
		t.Run(fmt.Sprintf("pending-%d", status), func(t *testing.T) {
			page, posts, key := restore(t)
			page.MustEval(`() => {const b=document.getElementById('message-body');b.form.requestSubmit();b.value='follow-up';b.form.requestSubmit();}`)
			resend := receivePending(t, page, posts)
			if resend.values.Get("idempotency_key") != key || resend.values.Get("body") != "hello" {
				t.Fatal("edited resend changed its key or text")
			}
			resend.reply <- browserReply{status: status, body: []byte("refused")}
			page.MustWait(`() => window.answers===2`)
			if page.MustEval(`() => {const f=document.querySelector('[data-pending-form]'), a=document.getElementById('message-send-alert');return f.querySelector('[role=status]').textContent!==a.dataset.stillUnconfirmed || f.querySelector('[role=status]').textContent===a.dataset.notSent;}`).Bool() {
				t.Fatal("refused edited resend did not announce that the earlier send is still unconfirmed")
			}
			if page.MustEval(`() => window.requests!==2 || document.querySelectorAll('[data-state=queued]').length!==1`).Bool() || len(posts) != 0 {
				t.Fatal("later message escaped the edited unconfirmed item's pause")
			}
			if page.MustEval(`() => document.getElementById('message-composer').dataset.unconfirmedKey!==undefined`).Bool() {
				t.Fatal("pending resend left the unconfirmed mark on the composer")
			}
			page.MustElement("[data-pending-actions] button[type=submit]").MustClick()
			retry := receivePending(t, page, posts)
			if retry.values.Encode() != resend.values.Encode() || page.MustEval(`key => document.querySelector('[data-pending-form]').elements.idempotency_key.value!==key || window.requests!==3`, key).Bool() {
				t.Fatal("unconfirmed Retry moved behind the later message or changed its send")
			}
			retry.reply <- pendingAnswer(t, kernel.ID{51})
			next := receivePending(t, page, posts)
			if next.values.Get("body") != "follow-up" || next.values.Get("idempotency_key") == key {
				t.Fatal("confirmed Retry did not resume the later message")
			}
			next.reply <- pendingAnswer(t, kernel.ID{52})
			page.MustWait(`() => window.answers===4`)
		})
		t.Run(fmt.Sprintf("ordinary-%d", status), func(t *testing.T) {
			page, posts, key := restore(t)
			page.MustEval(`text => {
 window.ordinaryAnswers=0;
 document.addEventListener('htmx:afterRequest',e=>{if(e.target.id==='message-composer') window.ordinaryAnswers++;});
 const b=document.getElementById('message-body');b.value=text;b.dispatchEvent(new Event('input',{bubbles:true}));b.form.requestSubmit();
}`, strings.Repeat("x", 4001))
			resend := receivePending(t, page, posts)
			if resend.values.Get("idempotency_key") != key || page.MustEval(`() => window.ordinaryRequests!==1 || !!document.querySelector('[data-pending-form]')`).Bool() {
				t.Fatal("over-limit edit did not use the ordinary path with its restored key")
			}
			resend.reply <- browserReply{status: status, body: []byte(renderIn(t, "en", view.MessageComposer(view.ChannelPage{IdempotencyKey: &key, Composer: new("generation"), BodyError: "refused", EventCursor: new(int64(42))})))}
			page.MustWait(`() => window.ordinaryAnswers===1`)
			if page.MustEval(`key => {const f=document.getElementById('message-composer'), a=document.getElementById('message-send-alert');return a.textContent!==a.dataset.stillUnconfirmed || a.textContent===a.dataset.notSent || f.dataset.unconfirmedKey!==key || f.elements.idempotency_key.value!==key;}`, key).Bool() {
				t.Fatal("ordinary refused edit lost the unconfirmed outcome")
			}
		})
	}
	t.Run("empty-clears-mark", func(t *testing.T) {
		page, posts, key := restore(t)
		if page.MustEval(`key => document.getElementById('message-composer').dataset.unconfirmedKey!==key`, key).Bool() {
			t.Fatal("restored text is missing its unconfirmed mark")
		}
		page.MustEval(`() => {const b=document.getElementById('message-body');b.value='';b.dispatchEvent(new Event('input',{bubbles:true}));}`)
		if page.MustEval(`key => {const f=document.getElementById('message-composer');return f.dataset.unconfirmedKey!==undefined || f.elements.idempotency_key.value===key;}`, key).Bool() {
			t.Fatal("emptying the restored text retained its unconfirmed mark or key")
		}
		page.MustEval(`() => {const b=document.getElementById('message-body');b.value='replacement';b.dispatchEvent(new Event('input',{bubbles:true}));b.form.requestSubmit();}`)
		next := receivePending(t, page, posts)
		next.reply <- browserReply{status: 422, body: []byte("refused")}
		page.MustWait(`() => window.answers===2`)
		if next.values.Get("idempotency_key") == key || page.MustEval(`() => document.querySelector('[data-pending-form] [role=status]').textContent!==document.getElementById('message-send-alert').dataset.notSent`).Bool() {
			t.Fatal("replacement inherited the discarded send's identity or uncertainty")
		}
	})
	t.Run("known-refusal-clears-mark", func(t *testing.T) {
		page, _, posts, _ := pendingBrowser(t, browser, false)
		page.MustEval(`() => {const b=document.getElementById('message-body');b.value='refused';b.form.requestSubmit();}`)
		first := receivePending(t, page, posts)
		first.reply <- browserReply{status: 422, body: []byte("refused")}
		page.MustWait(`() => window.answers===1`)
		// Isolate Edit's cleanup from the input and pending-submit cleanups.
		page.MustEval(`key => {document.getElementById('message-composer').dataset.unconfirmedKey=key;}`, first.values.Get("idempotency_key"))
		page.MustElement("[data-pending-edit]").MustClick()
		if page.MustEval(`() => document.getElementById('message-composer').dataset.unconfirmedKey!==undefined`).Bool() {
			t.Fatal("editing a known refusal kept a stale unconfirmed mark")
		}
		page.MustEval(`() => document.getElementById('message-composer').requestSubmit()`)
		resend := receivePending(t, page, posts)
		resend.reply <- browserReply{status: 429, body: []byte("refused")}
		page.MustWait(`() => window.answers===2`)
		if page.MustEval(`() => document.querySelector('[data-pending-form] [role=status]').textContent!==document.getElementById('message-send-alert').dataset.notSent`).Bool() {
			t.Fatal("known refusal became unconfirmed after Edit")
		}
	})
}
