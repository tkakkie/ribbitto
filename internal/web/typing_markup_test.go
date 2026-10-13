package web

import (
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/typing"
	"github.com/tkakkie/ribbitto/internal/web/i18n"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

func TestTypingStatusMarkup(t *testing.T) {
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"en", "ja"} {
		for _, mode := range []string{"feed", "topic", "older", "older-topic", "members", "no-stream"} {
			t.Run(lang+"/"+mode, func(t *testing.T) {
				page := view.ChannelPage{EventCursor: new(int64(42)), Current: view.Channel{ID: kernel.ID{1}}}
				if strings.Contains(mode, "topic") {
					page.Topic = &view.Topic{ID: kernel.ID{2}}
				}
				if strings.HasPrefix(mode, "older") {
					page.Before = 40
				}
				if mode == "members" {
					page.Panel, page.Members = view.PanelMembers, &view.MemberPage{PresenceAfter: "process:0"}
				}
				if mode == "no-stream" {
					page.EventCursor = nil
				}
				r := httptest.NewRequest("GET", "/", nil)
				r.Header.Set("Accept-Language", lang)
				w := httptest.NewRecorder()
				catalogues.Middleware(templ.Handler(view.ChannelScreen("", page))).ServeHTTP(w, r)
				doc, err := html.Parse(w.Body)
				if err != nil {
					t.Fatal(err)
				}
				want, count := mode == "feed" || mode == "topic" || mode == "members", 0
				for n := range doc.Descendants() {
					if attr(n, "id") != "typing-status" {
						continue
					}
					count++
					if attr(n, "sse-swap") != "typing" || attr(n, "hx-swap") != "innerHTML" ||
						attr(n, "role") != "status" || attr(n, "aria-live") != "polite" || attr(n, "aria-atomic") != "true" ||
						attr(n, "aria-label") != map[string]string{"en": "Typing activity", "ja": "入力状況"}[lang] || text(n) != "" {
						t.Fatal("typing must replace empty, localized polite status content")
					}
					stream := false
					for p := n; p != nil; p = p.Parent {
						if attr(p, "id") == "organization-stream" {
							stream = true
						}
						if attr(p, "id") == "message-composer" || attr(p, "id") == "message-list" {
							t.Fatal("typing inside a replacement or history")
						}
					}
					if !stream {
						t.Fatal("typing outside organization stream")
					}
				}
				if count != 0 && !want || count != 1 && want {
					t.Fatalf("typing regions = %d, want present %t", count, want)
				}
			})
		}
	}
}

// The exact text, spaces included: Japanese shows any space templ inserts
// between pieces around 、 and before が入力中.
func TestTypingIndicatorText(t *testing.T) {
	catalogues, err := i18n.New(slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	named := []typing.Typist{{DisplayName: "Alice", Handle: "alice"}}
	three := []typing.Typist{{Handle: "n3"}, {Handle: "n2"}, {Handle: "n1"}}
	for _, tt := range []struct {
		lang     string
		snapshot typing.Snapshot
		want     string
	}{
		{"en", typing.Snapshot{Typists: named}, "Alice @alice is typing…"},
		{"en", typing.Snapshot{Typists: three}, "@n3, @n2 and @n1 are typing…"},
		{"en", typing.Snapshot{Typists: three, Remaining: 2}, "@n3, @n2, @n1 and 2 others are typing…"},
		{"ja", typing.Snapshot{Typists: named}, "Alice @aliceが入力中…"},
		{"ja", typing.Snapshot{Typists: three}, "@n3、@n2、@n1が入力中…"},
		{"ja", typing.Snapshot{Typists: three, Remaining: 2}, "@n3、@n2、@n1、ほか2人が入力中…"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept-Language", tt.lang)
		w := httptest.NewRecorder()
		catalogues.Middleware(templ.Handler(view.TypingIndicator(tt.snapshot))).ServeHTTP(w, r)
		doc, err := html.Parse(w.Body)
		if err != nil {
			t.Fatal(err)
		}
		if got := text(doc); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.lang, got, tt.want)
		}
	}
}
