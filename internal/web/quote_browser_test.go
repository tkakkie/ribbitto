package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-rod/rod"
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
	choose := func(message conversation.Message) {
		t.Helper()
		page.MustElement(fmt.Sprintf(`[popovertarget="message-actions-%x"]`, message.ID)).MustClick()
		page.MustElement(fmt.Sprintf(`#message-actions-%x [name=quote]`, message.ID)).MustClick()
		page.MustElement(`#composer-quote input[name=quote]`)
		page.MustWait(`id => document.querySelector('#composer-quote input').value === id`, realtime.FormatPayloadID(message.ID))
		if scripts {
			page.MustWait(`() => document.activeElement.id === 'message-body' && !document.querySelector(':popover-open')`)
			if !page.MustEval(`() => window.quoteVisit && !location.search`).Bool() {
				t.Fatal("Quote navigated")
			}
		}
	}
	choose(first)
	page.MustElement("#composer-quote button").MustClick()
	page.MustWait(`() => !document.querySelector('#composer-quote input')`)
	if scripts {
		second = post("live original")
		emit(second.EventSeq)
		page.MustElement("#" + view.MessageDOMID(second.ID))
	}
	choose(second)
	page.MustElement("#message-body").MustInput("quoted answer")
	page.MustElement(`#message-composer button[aria-label]`).MustClick()
	page.MustElement("#message-items figure blockquote").MustWaitVisible()
	if got := page.MustElement("#message-items figure blockquote").MustText(); got != second.Body {
		t.Fatalf("posted quote %q", got)
	}
	page.MustWait(`() => !document.querySelector('#composer-quote input')`)
	if scripts && !page.MustEval(`() => window.quoteVisit && !location.search`).Bool() {
		t.Fatal("posting navigated")
	}
}
