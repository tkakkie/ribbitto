package web

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/unread"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

// readControlVisit's snapshot reports its first unread sequences until a
// reading POST arrives. Persistence has its own DB tests.
type readControlVisit struct {
	fakeMessages
	feed, topic int64
	reads       atomic.Int32
}

func (v *readControlVisit) Feed(context.Context, unread.Scope, int64, int64) error {
	v.reads.Add(1)
	return nil
}

func (v *readControlVisit) Topic(context.Context, unread.TopicScope, int64, int64) error {
	v.reads.Add(1)
	return nil
}

func (v *readControlVisit) Overview(ctx context.Context, m org.Membership, channel kernel.ID, topic *kernel.ID, before *int64, _ int64) (conversation.ChannelPage, error) {
	return v.Page(ctx, m, channel, topic, before)
}

func (v *readControlVisit) Page(ctx context.Context, m org.Membership, channel kernel.ID, topic *kernel.ID, before *int64) (conversation.ChannelPage, error) {
	page, err := v.fakeMessages.Page(ctx, m, channel, topic, before)
	if v.reads.Load() == 0 {
		page.FeedFirstUnread, page.TopicFirstUnread = v.feed, v.topic
	}
	return page, err
}

// readControl returns the page's read form and its submit buttons, failing
// when the form automatic reading posts through is missing.
func readControl(t *testing.T, body *bytes.Buffer) (*html.Node, []*html.Node) {
	t.Helper()
	doc, err := html.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if problems := checkMarkup(doc, true); len(problems) != 0 {
		t.Fatal(problems)
	}
	var form *html.Node
	for n := range doc.Descendants() {
		if attr(n, "id") == "read-form" {
			form = n
		}
	}
	if form == nil {
		t.Fatal("no #read-form for automatic reading")
	}
	var cursor bool
	var buttons []*html.Node
	for n := range form.Descendants() {
		cursor = cursor || attr(n, "name") == "cursor" && attr(n, "type") == "hidden" && attr(n, "value") == "42"
		if n.Type == html.ElementNode && n.Data == "button" {
			buttons = append(buttons, n)
		}
	}
	if !cursor {
		t.Fatal("read form lost its snapshot cursor")
	}
	return form, buttons
}

func TestReadControlOnlyWhenUnread(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name        string
		messages    fakeMessages
		feed, topic int64
	}{
		{"empty channel", fakeMessages{}, 0, 0},
		{"fully read", populatedMessages(), 0, 0},
		{"unread", populatedMessages(), 7, 8},
		// Another topic's unread message is no reason to offer it in this one.
		{"other topic unread", populatedMessages(), 7, 0},
	} {
		for _, topic := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/topic=%t", tt.name, topic), func(t *testing.T) {
				visit := &readControlVisit{fakeMessages: tt.messages, feed: tt.feed, topic: tt.topic}
				h, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Messages, s.Reading = visit, visit }))
				if err != nil {
					t.Fatal(err)
				}
				path, first := view.ChannelURL("acme", kernel.ID{1}), tt.feed
				if topic {
					path, first = view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{5}), tt.topic
				}
				get := func(panel string) *httptest.ResponseRecorder {
					r := httptest.NewRequest("GET", path+panel, nil)
					r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					if w.Code != 200 {
						t.Fatalf("GET status %d", w.Code)
					}
					return w
				}
				assertHidden := func(w *httptest.ResponseRecorder) {
					t.Helper()
					body := w.Body.String()
					form, buttons := readControl(t, w.Body)
					if _, hidden := attrOK(form, "hidden"); !hidden || len(buttons) != 0 || strings.Contains(body, "Mark all as read") {
						t.Fatalf("control shown with nothing unread: hidden=%t buttons=%d", hidden, len(buttons))
					}
				}
				// The overview submits the same #read-form from outside it, once
				// and only when the header offers Mark all as read (#972).
				doc, err := html.Parse(get("/overview").Body)
				if err != nil {
					t.Fatal(err)
				}
				sheet := 0
				for n := range doc.Descendants() {
					if attr(n, "form") == "read-form" && strings.TrimSpace(text(n)) == "Mark all as read" {
						sheet++
					}
				}
				if problems := checkMarkup(doc, true); len(problems) != 0 || sheet != int(min(first, 1)) {
					t.Fatalf("overview: %d sheet controls, markup %q", sheet, problems)
				}
				w := get("")
				if first == 0 {
					assertHidden(w)
					return
				}
				form, buttons := readControl(t, w.Body)
				if _, hidden := attrOK(form, "hidden"); hidden || len(buttons) != 1 || attr(buttons[0], "type") != "submit" || strings.TrimSpace(text(buttons[0])) != "Mark all as read" {
					t.Fatalf("no plain control with something unread: hidden=%t buttons=%d", hidden, len(buttons))
				}
				// Without JavaScript the button submits the plain form, which
				// reads and redirects to a page that has nothing left to mark.
				if attr(form, "method") != "post" || attr(form, "action") != path+"/read" {
					t.Fatalf("plain form posts %s to %s", attr(form, "method"), attr(form, "action"))
				}
				posted := serveForm(h, "POST", attr(form, "action"), "live", url.Values{"cursor": {"42"}})
				if posted.Code != http.StatusSeeOther || posted.Header().Get("Location") != path || visit.reads.Load() != 1 {
					t.Fatalf("plain read: status %d, location %q, reads %d", posted.Code, posted.Header().Get("Location"), visit.reads.Load())
				}
				assertHidden(get(""))
			})
		}
	}
}

// The Japanese label is pinned like the English one, on the header control and
// on the overview's copy of it, so a reverted or half-updated translation fails.
func TestReadControlJapaneseLabel(t *testing.T) {
	for _, topic := range []bool{false, true} {
		visit := &readControlVisit{fakeMessages: populatedMessages(), feed: 7, topic: 8}
		h, _ := newLimitedHandler(t, asAlice, func(s *Services) { s.Messages, s.Reading = visit, visit })
		path := view.ChannelURL("acme", kernel.ID{1})
		if topic {
			path = view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{5})
		}
		for _, tc := range []struct {
			panel   string
			buttons int
		}{{"", 1}, {"/overview", 2}} {
			r := httptest.NewRequest("GET", path+tc.panel, nil)
			r.Header.Set("Accept-Language", "ja")
			r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			doc, err := html.Parse(w.Body)
			if err != nil {
				t.Fatal(err)
			}
			var labels []string
			for n := range doc.Descendants() {
				if n.Type == html.ElementNode && n.Data == "button" && (attr(n, "form") == "read-form" || n.Parent != nil && attr(n.Parent, "id") == "read-form") {
					labels = append(labels, strings.TrimSpace(text(n)))
				}
			}
			if w.Code != 200 || len(labels) != tc.buttons {
				t.Fatalf("topic=%t %q: status %d, labels %q", topic, tc.panel, w.Code, labels)
			}
			for _, label := range labels {
				if label != "すべて既読にする" {
					t.Fatalf("topic=%t %q: ja label %q", topic, tc.panel, label)
				}
			}
		}
	}
}
