package web

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func TestOverviewPins(t *testing.T) {
	at := time.Date(2026, 10, 16, 9, 30, 0, 0, time.UTC)
	pins := []conversation.Pin{
		// A literal tag and right-to-left text stay text, isolated by dir="auto".
		{Entry: conversation.Entry{Message: conversation.Message{ID: kernel.ID{0xab}, Body: `<b dir="rtl">x</b> مرحبا`}, DisplayName: "مريم", Handle: "author", TopicName: "<design>"}, PinnerName: "Bob", PinnerHandle: "bob", PinnedAt: at},
		{Entry: conversation.Entry{Message: conversation.Message{ID: kernel.ID{0xcd}, Body: "second"}, Handle: "legacy", DefaultTopic: true}, PinnerName: "Bob", PinnerHandle: "bob", PinnedAt: at.Add(-time.Hour)},
	}
	labels := map[string][3]string{
		"en": {"Pinned messages", "No pinned messages yet.", "Pinned by"},
		"ja": {"ピン留めしたメッセージ", "ピン留めしたメッセージはまだありません。", "ピン留め:"},
	}
	// The feed's and a topic's overview both list every topic's pins.
	for _, path := range []string{view.ChannelURL("acme", kernel.ID{1}) + "/overview", view.ConversationURL("acme", kernel.ID{1}, &kernel.ID{2}) + "/overview"} {
		for lang, want := range labels {
			for _, listed := range [][]conversation.Pin{pins, nil} {
				handler, _ := newLimitedHandler(t, asAlice, func(s *Services) { s.Messages = fakeMessages{pins: listed} })
				r := httptest.NewRequest(http.MethodGet, path, nil)
				r.Header.Set("Accept-Language", lang)
				r.AddCookie(&http.Cookie{Name: middleware.SessionCookie, Value: "live"})
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				doc, err := html.Parse(w.Body)
				var section *html.Node
				for n := range doc.Descendants() {
					if attr(n, "id") == "pins-heading" && text(n) == want[0] {
						section = n.Parent
					}
				}
				if w.Code != http.StatusOK || err != nil || section == nil {
					t.Fatalf("%s %s: status %d, %v, pins section %v", path, lang, w.Code, err, section != nil)
				}
				body := text(section)
				if listed == nil {
					if !strings.Contains(body, want[1]) || find(section, atom.Ol) != nil {
						t.Fatalf("%s %s: empty state = %q", path, lang, body)
					}
					continue
				}
				var links []string
				for n := range section.Descendants() {
					if n.DataAtom == atom.A && attr(n, "dir") == "auto" && find(n, atom.B) == nil {
						links = append(links, attr(n, "href")+" "+text(n))
					}
				}
				if want := []string{view.MessageURL("acme", kernel.ID{0xab}) + " " + pins[0].Body, view.MessageURL("acme", kernel.ID{0xcd}) + " second"}; !slices.Equal(links, want) {
					t.Fatalf("%s %s: isolated plain-text links %q, want %q", path, lang, links, want)
				}
				for _, part := range []string{"مريم", "@author", "<design>", "@legacy", "chorus", want[2], "Bob", "@bob", "2026-10-16 09:30:00 UTC"} {
					if !strings.Contains(body, part) {
						t.Fatalf("%s %s: entry text %q lacks %q", path, lang, body, part)
					}
				}
			}
		}
	}
}
