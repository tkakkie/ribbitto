package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func testQuoteBrowser(t *testing.T, browser *rod.Browser, scripts bool, width int) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool := pgtest.New(t)
	f := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
	m := org.Membership{Organization: org.Organization{ID: f.OrganizationID, Slug: "acme"}, Member: org.Member{ID: f.MemberID}}
	sessions := identitypg.NewSessions(pool, time.Now, nil)
	services := postgresServices(t, pool, sessions, "", false)
	cookie, _, err := sessions.Create(ctx, f.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	post := func(body string) conversation.Message {
		t.Helper()
		sent, err := services.Posting.Post(ctx, m, f.Channel.ID, body)
		if err != nil {
			t.Fatal(err)
		}
		return sent
	}
	first := post("page original")
	second := post("another original")
	catalogues, err := i18n.New(slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler("", catalogues, services)
	if err != nil {
		t.Fatal(err)
	}
	frames := make(chan realtime.Outgoing, 4)
	feed := view.ChannelURL("acme", f.Channel.ID)
	emit := func(seq int64) {
		entry, err := services.Messages.One(ctx, m, f.Channel.ID, seq)
		if err != nil {
			t.Error(err)
			return
		}
		frames <- realtime.Outgoing{ID: seq, Name: "message", Data: streamTestMarkup(t, view.LiveMessageItem(viewMessage("acme", entry)))}
	}
	quoteHolds, postHolds := make(chan chan struct{}, 1), make(chan chan struct{}, 1)
	stop := make(chan struct{})
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/organizations/acme/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			sender := sseSender{w: w, rc: http.NewResponseController(w), timeout: time.Second}
			if sender.Heartbeat(r.Context()) != nil {
				return
			}
			for {
				select {
				case frame := <-frames:
					if sender.Send(r.Context(), frame) != nil {
						return
					}
				case <-r.Context().Done():
					return
				}
			}
		}
		if scripts && r.Method == "POST" && (r.URL.Path == feed || r.URL.Path == feed+"/quote") {
			holds := quoteHolds
			if r.URL.Path == feed {
				posts.Add(1)
				holds = postHolds
			}
			select {
			case release := <-holds:
				select {
				case <-release:
				case <-stop:
					return
				case <-r.Context().Done():
					return
				}
			default:
			}
		}
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: cookie})
		handler.ServeHTTP(w, r)
		if scripts && r.Method == "POST" && r.URL.Path == feed {
			history, err := services.Messages.Page(ctx, m, f.Channel.ID, nil, nil)
			if err != nil {
				t.Error(err)
				return
			}
			emit(history.Entries[len(history.Entries)-1].EventSeq)
		}
	}))
	defer server.Close()
	defer close(stop)
	page := browser.MustPage().Context(ctx).WithPanic(func(v any) { t.Fatal(v) })
	defer page.MustClose()
	page.MustSetViewport(width, 844, 1, width < 768)
	if !scripts {
		router := page.HijackRequests()
		router.MustAdd("*.js", func(h *rod.Hijack) { h.Response.Fail(proto.NetworkErrorReasonBlockedByClient) })
		go router.Run()
		defer router.MustStop()
	}
	page.MustNavigate(server.URL + feed).MustWaitLoad()
	page.MustEval(`() => window.quoteVisit = true`)
	choose := func(message conversation.Message, hold bool) {
		t.Helper()
		release := make(chan struct{})
		if hold {
			quoteHolds <- release
		}
		page.MustElement(fmt.Sprintf(`[popovertarget="message-actions-%x"]`, message.ID)).MustClick()
		page.MustElement(fmt.Sprintf(`#message-actions-%x [name=quote]`, message.ID)).MustClick()
		if hold {
			page.MustWait(`() => document.getElementById('quote-form').classList.contains('htmx-request')`)
			page.MustElement("#message-body").MustFocus()
			page.Keyboard.MustType(input.Enter)
			if problem := page.MustEval(`() => {
 const b = document.getElementById('message-body');
 if (b.value !== 'quoted answer') return 'Enter cleared the draft during quote selection';
 if (document.querySelector('#message-pending > li')) return 'Enter queued a post during quote selection';
 if (b.form.classList.contains('htmx-request')) return 'Enter posted during quote selection';
 return '';
}`).Str(); problem != "" {
				t.Fatal(problem)
			}
			close(release)
		}
		page.MustElement(`#composer-quote input[name=quote]`)
		page.MustWait(`id => document.querySelector('#composer-quote input').value === id`, realtime.FormatPayloadID(message.ID))
		if scripts {
			page.MustWait(`() => document.activeElement.id === 'message-body' && !document.querySelector(':popover-open')`)
			if !page.MustEval(`() => window.quoteVisit && !location.search`).Bool() {
				t.Fatal("Quote navigated")
			}
		}
	}
	choose(first, false)
	page.MustElement("#composer-quote button").MustClick()
	page.MustWait(`() => !document.querySelector('#composer-quote input')`)
	if scripts {
		page.MustWait(`() => document.activeElement.id === 'message-body'`)

		second = post("live original")
		emit(second.EventSeq)
		page.MustElement("#" + view.MessageDOMID(second.ID))
	}
	if scripts {
		page.MustElement("#message-body").MustInput("quoted answer")
		// Exercise both the empty-chip pending path and an existing quoted send.
		choose(first, true)
	}
	choose(second, scripts)
	if !scripts {
		page.MustElement("#message-body").MustInput("quoted answer")
	}
	release := make(chan struct{})
	if scripts {
		postHolds <- release
	}
	page.MustElement(`#message-composer button[type="submit"]:not([form])`).MustClick()
	if scripts {
		page.MustWait(`() => document.getElementById('message-composer').classList.contains('htmx-request')`)
		if problem := page.MustEval(`() => {
 const send = document.querySelector('#message-composer button[type="submit"]:not([form])');
 if (!send.disabled) return 'Send is enabled during a quoted post';
 if (document.querySelector('#composer-quote button').disabled) return 'Remove quote was disabled instead of Send';
 return '';
}`).Str(); problem != "" {
			t.Fatal(problem)
		}
		close(release)
	}
	page.MustElement("#message-items figure blockquote").MustWaitVisible()
	if got := page.MustElement("#message-items figure blockquote").MustText(); got != second.Body {
		t.Fatalf("posted quote %q", got)
	}
	page.MustWait(`() => !document.querySelector('#composer-quote input')`)
	if scripts {
		page.MustWait(`() => !document.querySelector('#message-composer button[type="submit"]:not([form])').disabled && !document.getElementById('message-composer').classList.contains('htmx-request')`)
		if got := posts.Load(); got != 1 {
			t.Fatalf("posts = %d, want only the deliberate quoted send", got)
		}
	}
	if scripts && !page.MustEval(`() => window.quoteVisit && !location.search`).Bool() {
		t.Fatal("posting navigated")
	}
}
