package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type noticeReader struct {
	sidebarReader
	calls []string
}

func (s *noticeReader) Page(_ context.Context, _ org.Membership, _ kernel.ID, _ *kernel.ID, before *int64) (conversation.ChannelPage, error) {
	call := "latest"
	if before != nil {
		call = fmt.Sprintf("before=%d", *before)
	}
	s.calls = append(s.calls, call)
	return s.page, nil
}

func (s *noticeReader) Around(_ context.Context, _ org.Membership, _ kernel.ID, _ *kernel.ID, around int64) (conversation.ChannelPage, error) {
	s.calls = append(s.calls, fmt.Sprintf("around=%d", around))
	return s.page, nil
}

func TestUnreadAbove(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		for _, topic := range []bool{false, true} {
			for _, tt := range []struct {
				name, query, call  string
				first              int64
				empty, list, shown bool
			}{
				{name: "latest", call: "latest", first: 30, shown: true},
				{name: "adjacent", call: "latest", first: 39, shown: true},
				{name: "equal", call: "latest", first: 40},
				{name: "newer", call: "latest", first: 60},
				{name: "zero", call: "latest"},
				{name: "around zero", query: "?around=50&unread=0", call: "around=50"},
				{name: "empty", call: "latest", first: 30, empty: true},
				{name: "before", query: "?before=90&unread=30", call: "before=90", shown: true},
				{name: "before newer", query: "?before=90&unread=60", call: "before=90"},
				{name: "around other", query: "?around=50&unread=30", call: "around=50", shown: true},
				{name: "ordinary moved out", query: "?around=30", call: "around=30", first: 30, shown: true},
				{name: "moved out", query: "?around=30&unread=30", call: "around=30", first: 30},
				{name: "fabricated", query: "?around=29&unread=29", call: "around=29"},
				{name: "list", first: 30, list: true},
			} {
				t.Run(fmt.Sprintf("%s/topic=%t/%s", lang, topic, tt.name), func(t *testing.T) {
					entries := populatedMessages().entries
					entries[0].EventSeq, entries[1].EventSeq = 40, 50
					entries[0].TopicID, entries[1].TopicID = kernel.ID{5}, kernel.ID{5}
					if tt.empty {
						entries = nil
					}
					reader := &noticeReader{sidebarReader: sidebarReader{page: conversation.ChannelPage{Current: conversation.Channel{ID: kernel.ID{1}}, Page: conversation.Page{Entries: entries}, PageCounts: conversation.PageCounts{FeedFirstUnread: tt.first, TopicFirstUnread: tt.first}}}}
					path := view.ChannelURL("acme", kernel.ID{1})
					if topic {
						reader.page.Topic = &conversation.Topic{ID: kernel.ID{5}}
						path = view.ConversationURL("acme", kernel.ID{1}, &reader.page.Topic.ID)
					}
					if tt.list {
						path = "/organizations/acme/channels"
					}
					reading := &dividerVisit{}
					handler, _ := newLimitedHandler(t, asAlice, func(s *Services) { s.Messages, s.Reading = reader, reading })
					r := httptest.NewRequest("GET", path+tt.query, nil)
					r.Header.Set("Accept-Language", lang)
					r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, r)
					if w.Code != 200 {
						t.Fatalf("status %d", w.Code)
					}
					if reading.read.Load() || (!tt.list && fmt.Sprint(reader.calls) != "["+tt.call+"]") {
						t.Fatalf("GET service calls: %v, read=%t", reader.calls, reading.read.Load())
					}
					doc, err := html.Parse(w.Body)
					if err != nil {
						t.Fatal(err)
					}
					if problems := checkMarkup(doc, true); len(problems) > 0 {
						t.Fatal(problems)
					}
					var notice *html.Node
					for n := range doc.Descendants() {
						if n.DataAtom == atom.A && strings.HasSuffix(attr(n, "href"), "#unread-divider") {
							notice = n.Parent
						}
					}
					if (notice != nil) != tt.shown {
						t.Fatalf("notice present=%t, want %t", notice != nil, tt.shown)
					}
					if notice == nil {
						if (tt.name == "moved out" || tt.name == "fabricated") && strings.Contains(w.Body.String(), `id="unread-divider"`) {
							t.Fatal("absent target has divider")
						}
						return
					}
					link := find(notice, atom.A)
					first := tt.first
					if tt.query != "" {
						first = 30
					}
					want := fmt.Sprintf("%s?around=%d&unread=%d#unread-divider", path, first, first)
					above, jump := "Unread above", "Jump to first unread"
					if lang == "ja" {
						above, jump = "上に未読があります", "最初の未読へ移動"
					}
					if attr(link, "href") != want || text(find(notice, atom.P)) != above || text(link) != jump || strings.TrimSpace(text(notice)) != above+jump {
						t.Fatalf("notice text/href: %q %q", text(notice), attr(link, "href"))
					}
					elements := 1
					for n := range notice.Descendants() {
						if n.Type != html.ElementNode {
							continue
						}
						elements++
						if n.DataAtom != atom.P && n.DataAtom != atom.A {
							t.Fatal("unexpected notice element")
						}
					}
					for _, n := range append([]*html.Node{notice}, find(notice, atom.P), link) {
						for _, a := range n.Attr {
							if a.Key == "role" || a.Key == "aria-live" || a.Key == "autofocus" || strings.HasPrefix(a.Key, "hx-") {
								t.Fatalf("active notice attribute %s", a.Key)
							}
						}
					}
					if elements != 3 || notice.DataAtom != atom.Div {
						t.Fatalf("notice wrapper=%s, elements=%d", notice.Data, elements)
					}
					// Direct siblings keep it outside list, history and stream swap regions.
					prev := notice.PrevSibling
					for prev != nil && prev.Type != html.ElementNode {
						prev = prev.PrevSibling
					}
					next := notice.NextSibling
					for next != nil && attr(next, "id") != "message-list" {
						next = next.NextSibling
					}
					if prev == nil || prev.DataAtom != atom.Header || next == nil || notice.Parent != next.Parent {
						t.Fatal("notice order or swap boundary")
					}
				})
			}
		}
	}
}
