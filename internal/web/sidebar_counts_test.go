package web

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type sidebarReader struct {
	fakeMessages
	page conversation.ChannelPage
}

func (s sidebarReader) Page(context.Context, org.Membership, kernel.ID, *kernel.ID, *int64) (conversation.ChannelPage, error) {
	return s.page, nil
}

func (s sidebarReader) Members(context.Context, org.Membership, kernel.ID, *kernel.ID) (conversation.MembersPage, error) {
	return conversation.MembersPage{Current: s.page.Current, Channels: s.page.Channels, Topics: s.page.Topics, PageCounts: s.page.PageCounts, EventCursor: s.page.EventCursor}, nil
}

func TestSidebarCountsMarkup(t *testing.T) {
	catalogues, err := i18n.New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"en", "ja"} {
		for _, kind := range []string{"feed", "feed older", "topic", "topic older", "unlisted", "unlisted older", "members"} {
			t.Run(lang+"/"+kind, func(t *testing.T) {
				page := conversation.ChannelPage{EventCursor: new(int64(42)), PageCounts: conversation.PageCounts{
					ChannelCounts: map[kernel.ID]int64{}, TopicCounts: map[kernel.ID]int64{},
				}}
				counts := []int64{0, 1, 99, 100}
				for i, count := range counts {
					id := kernel.ID{byte(i + 1)}
					page.Channels = append(page.Channels, conversation.Channel{ID: id, Name: "<channel>"})
					page.ChannelCounts[id] = count
				}
				page.Current = page.Channels[1]
				for i := range 50 {
					id := kernel.ID{byte(i + 1)}
					page.Topics = append(page.Topics, conversation.Topic{ID: id, Name: "<topic>", IsDefault: i == 0})
					page.TopicCounts[id] = counts[i%4]
				}
				path := view.ChannelURL("acme", page.Current.ID)
				if strings.HasPrefix(kind, "topic") {
					page.Topic = &page.Topics[2]
				}
				if strings.HasPrefix(kind, "unlisted") {
					page.Topic = &conversation.Topic{ID: kernel.ID{200}, Name: "<selected>"}
					page.TopicCounts[page.Topic.ID] = 100
				}
				if page.Topic != nil {
					path = view.ConversationURL("acme", page.Current.ID, &page.Topic.ID)
				}
				if strings.HasSuffix(kind, "older") {
					path += "?before=9"
				}
				if kind == "members" {
					path += "/members"
				}
				handler, err := NewHandler("", catalogues, testServices(asAlice, func(s *Services) { s.Messages = sidebarReader{page: page} }))
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest(http.MethodGet, path, nil)
				r.Header.Set("Accept-Language", lang)
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != http.StatusOK {
					t.Fatalf("status %d: %s", w.Code, w.Body.String())
				}
				doc, err := html.Parse(w.Body)
				if err != nil {
					t.Fatal(err)
				}
				if problems := checkMarkup(doc, true); len(problems) != 0 {
					t.Fatal(problems)
				}
				endpoint, _ := idAttribute(t, doc, "organization-stream", "sse-connect")
				sub, valid := streamScope(httptest.NewRequest("GET", endpoint, nil))
				if !valid || (sub.Topic == nil) != (page.Topic == nil) || (sub.Topic != nil && *sub.Topic != page.Topic.ID) ||
					strings.Contains(endpoint, "panel=") ||
					strings.Contains(endpoint, "messages") != !strings.HasSuffix(kind, "older") {
					t.Fatalf("page selection or interests lost: %s", endpoint)
				}
				var frame realtime.Outgoing
				catalogues.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
					frame, err = RenderSidebar(r.Context(), org.Membership{Organization: org.Organization{Slug: "acme"}}, conversation.SidebarSnapshot{
						Current: page.Current, Channels: page.Channels, Topics: page.Topics, Topic: page.Topic,
						ChannelCounts: page.ChannelCounts, TopicCounts: page.TopicCounts,
					}, kind == "members")
				})).ServeHTTP(httptest.NewRecorder(), r)
				if err != nil {
					t.Fatal(err)
				}
				assertSidebarMatchesPage(t, doc, frame)
				channels, topics := 0, 0
				for n := range doc.Descendants() {
					id := attr(n, "id")
					var count int64
					selected := false
					switch {
					case n.DataAtom == atom.Li && strings.HasPrefix(id, "channel-"):
						c := page.Channels[channels]
						if id != fmt.Sprintf("channel-%x", c.ID) {
							t.Fatal("unstable channel ID")
						}
						count, selected = page.ChannelCounts[c.ID], page.Topic == nil && c.ID == page.Current.ID
						channels++
					case n.DataAtom == atom.Li && strings.HasPrefix(id, "topic-"):
						topic := page.Topics[topics]
						if id != fmt.Sprintf("topic-%x", topic.ID) {
							t.Fatal("unstable topic ID")
						}
						count, selected = page.TopicCounts[topic.ID], page.Topic != nil && page.Topic.ID == topic.ID
						topics++
					default:
						continue
					}
					link := find(n, atom.A)
					if link == nil || attr(link, "id") != "link-"+id || attr(link, "href") == "" || (attr(link, "aria-current") == "page") != selected {
						t.Fatal("link or selection lost")
					}
					if count > 0 && !strings.Contains(attr(link, "class"), "font-semibold") {
						t.Fatal("unread entry has no weight distinction")
					}
					// The entry's own link: a channel row also nests its topic rows.
					assertUnreadBadge(t, link, lang, count)
				}
				if channels != 4 || topics != 50 {
					t.Fatalf("entries: channels=%d topics=%d", channels, topics)
				}
				if page.Topic != nil {
					if _, ok := idAttribute(t, doc, "topic-heading-count", "class"); !ok {
						t.Fatal("topic heading target missing")
					}
				}
				if kind != "members" {
					count := int64(0)
					if strings.HasPrefix(kind, "unlisted") {
						count = 100
					}
					assertUnreadBadge(t, find(doc, atom.H1), lang, count)
				}
			})
		}
	}
}

func assertUnreadBadge(t *testing.T, node *html.Node, lang string, count int64) {
	t.Helper()
	visible, accessible := "", ""
	for n := range node.Descendants() {
		// The badge pairs a hidden count with its screen-reader text; other
		// decorative spans (the row's tile, the selection pill) are not it.
		if n.DataAtom == atom.Span && attr(n, "aria-hidden") == "true" && hasSROnlyChild(n.Parent) {
			if n.FirstChild != nil {
				visible += n.FirstChild.Data
			}
		}
		if n.DataAtom == atom.Span && attr(n, "class") == "sr-only" {
			accessible += text(n)
		}
	}
	want, label := "", ""
	if count > 0 {
		want = fmt.Sprint(count)
		if count >= 100 {
			want = "99+"
		}
		label = " Unread messages: " + want
		if lang == "ja" {
			label = " 未読メッセージ " + want + " 件"
		}
	}
	if visible != want || accessible != label {
		t.Fatalf("badge: visible=%q label=%q, want %q %q", visible, accessible, want, label)
	}
}

func hasSROnlyChild(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.DataAtom == atom.Span && attr(c, "class") == "sr-only" {
			return true
		}
	}
	return false
}
