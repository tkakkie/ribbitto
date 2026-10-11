package web

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// Model changing snapshot results after the real handler accepts a reading POST.
// Read-state persistence and the first-unread queries have their own DB tests.
type dividerVisit struct {
	fakeMessages
	read  atomic.Bool
	first int64
}

func (v *dividerVisit) Feed(context.Context, unread.Scope, int64, int64) error {
	v.read.Store(true)
	return nil
}

func (v *dividerVisit) Topic(context.Context, unread.TopicScope, int64, int64) error {
	v.read.Store(true)
	return nil
}

func (v *dividerVisit) Page(ctx context.Context, m org.Membership, channel kernel.ID, topic *kernel.ID, before *int64) (conversation.ChannelPage, error) {
	page, err := v.fakeMessages.Page(ctx, m, channel, topic, before)
	seqs := []int{40, 50, 60}
	page.Older = true
	if before != nil {
		seqs = []int{20, 30, 35}
		if *before == 20 {
			seqs, page.Older = []int{5, 10}, false
		}
	}
	page.Entries = nil
	for _, msg := range streamTestMessages(kernel.ID{5}, seqs...) {
		page.Entries = append(page.Entries, conversation.Entry{Message: conversation.Message{ID: msg.ID, TopicID: msg.TopicID, EventSeq: msg.EventSeq, Body: msg.Body}})
	}
	if !v.read.Load() {
		page.FeedFirstUnread, page.TopicFirstUnread = v.first, v.first
	}
	return page, err
}

func testUnreadDividerBrowser(t *testing.T, browser *rod.Browser) {
	for _, topic := range []bool{false, true} {
		for _, first := range []int64{30, 50} {
			t.Run(fmt.Sprintf("topic=%t/first=%d", topic, first), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
				if err != nil {
					t.Fatal(err)
				}
				visit := &dividerVisit{first: first}
				handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Messages, s.Reading = visit, visit }))
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				page.MustEvalOnNewDocument(`document.addEventListener('htmx:afterRequest', e => {
                    if (e.detail.elt?.id === 'read-form' && e.detail.successful) document.documentElement.dataset.readComplete = 'true';
                });`)
				page.MustNavigate(server.URL + path).MustWaitLoad()
				// Observe the completed automatic POST, not merely request initiation.
				page.MustWait(`() => document.documentElement.dataset.readComplete === 'true'`)
				if !visit.read.Load() {
					t.Fatal("visible load did not read")
				}
				assertDivider := func(present bool) {
					t.Helper()
					page.MustWait(`(present, seq) => {
						const dividers = document.querySelectorAll('#unread-divider');
						return present ? dividers.length === 1 && dividers[0].nextElementSibling?.dataset.eventSeq === seq : dividers.length === 0;
					}`, present, fmt.Sprint(first))
				}
				assertDivider(first == 50)
				page.MustElement("#load-older a").MustClick()
				page.MustWait(`() => document.querySelector('#load-older').dataset.oldestSeq === '20'`)
				assertDivider(true)
				page.MustElement("#load-older a").MustClick()
				page.MustWait(`() => !document.querySelector('#load-older a')`)
				assertDivider(true)
				if page.MustEval(`() => document.querySelector('#message-status').textContent`).Str() != "" {
					t.Fatal("history announced the divider")
				}
				page.MustNavigate(server.URL + path).MustWaitLoad()
				assertDivider(false)
				href := page.MustElement("#load-older a").MustAttribute("href")
				if href == nil || !strings.HasSuffix(*href, "unread=0") {
					t.Fatal("fresh load did not carry zero")
				}
			})
		}
	}
}
