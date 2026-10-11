package web

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

func TestUnreadDivider(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"en", "ja"} {
		for _, topic := range []bool{false, true} {
			for _, tt := range []struct {
				name, query          string
				feed, selected, want int64
				status               int
			}{
				{"snapshot", "", 7, 8, 7, 200},
				{"read", "", 0, 0, 0, 200},
				{"outside page", "", 6, 6, 6, 200},
				{"direct older", "?before=40", 7, 7, 7, 200},
				{"visit after read", "?before=40&unread=8", 0, 0, 8, 200},
				{"visit without unread", "?before=40&unread=0", 7, 8, 0, 200},
				{"negative", "?before=40&unread=-1", 0, 0, 0, 400},
				{"empty", "?before=40&unread=", 0, 0, 0, 400},
				{"invalid", "?before=40&unread=x", 0, 0, 0, 400},
				{"overflow", "?before=40&unread=9223372036854775808", 0, 0, 0, 400},
				{"repeated", "?before=40&unread=7&unread=8", 0, 0, 0, 400},
				{"latest cannot carry", "?unread=8", 0, 0, 0, 400},
			} {
				t.Run(fmt.Sprintf("%s/topic=%t/%s", lang, topic, tt.name), func(t *testing.T) {
					page := conversation.ChannelPage{Current: conversation.Channel{ID: kernel.ID{1}},
						Page:       conversation.Page{Entries: populatedMessages().entries, Older: true},
						PageCounts: conversation.PageCounts{FeedFirstUnread: tt.feed, TopicFirstUnread: tt.selected}, EventCursor: new(int64(42))}
					path := view.ChannelURL("acme", page.Current.ID)
					want := tt.want
					if topic {
						page.Topic = &conversation.Topic{ID: kernel.ID{5}}
						path = view.ConversationURL("acme", page.Current.ID, &page.Topic.ID)
						if tt.name == "snapshot" {
							want = 8
						}
					}
					handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Messages = sidebarReader{page: page} }))
					if err != nil {
						t.Fatal(err)
					}
					r := httptest.NewRequest("GET", path+tt.query, nil)
					r.Header.Set("Accept-Language", lang)
					r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, r)
					if w.Code != tt.status {
						t.Fatalf("status %d, want %d", w.Code, tt.status)
					}
					if tt.status != 200 {
						return
					}
					doc, err := html.Parse(w.Body)
					if err != nil {
						t.Fatal(err)
					}
					if problems := checkMarkup(doc, true); len(problems) != 0 {
						t.Fatal(problems)
					}
					var divider *html.Node
					for n := range doc.Descendants() {
						if attr(n, "id") == "unread-divider" {
							divider = n
						}
						if attr(n, "hx-select") == "#message-items > li" {
							link := fmt.Sprintf("%s?before=7&unread=%d", path, want)
							if attr(n, "href") != link || attr(n, "hx-get") != link {
								t.Fatalf("carried link: %s", attr(n, "href"))
							}
						}
					}
					if want != 7 && want != 8 {
						if divider != nil {
							t.Fatal("divider without its message on the page")
						}
						return
					}
					label := map[string]string{"en": "New messages", "ja": "ここから未読メッセージ"}[lang]
					if divider == nil || strings.TrimSpace(text(divider)) != label || attr(divider.Parent, "id") != "message-items" {
						t.Fatal("missing localized page divider")
					}
					next := divider.NextSibling
					for next != nil && next.Type != html.ElementNode {
						next = next.NextSibling
					}
					if next == nil || attr(next, "data-event-seq") != fmt.Sprint(want) {
						t.Fatal("divider is not above the first unread message")
					}
					catalogues.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
						for _, entry := range page.Entries {
							var live bytes.Buffer
							if err := view.LiveMessageItem(viewMessage("acme", entry)).Render(r.Context(), &live); err != nil {
								t.Fatal(err)
							}
							if strings.Contains(live.String(), "unread-divider") || strings.Contains(live.String(), label) {
								t.Fatal("shared live render contains a divider")
							}
						}
					})).ServeHTTP(httptest.NewRecorder(), r)
				})
			}
		}
	}
}
