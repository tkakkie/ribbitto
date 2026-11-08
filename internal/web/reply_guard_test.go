package web

import (
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

// #2200 approves three elements per available reply and no htmx attributes.
const replyBudgetElements = 3

func TestReplyMarkupCost(t *testing.T) {
	for _, page := range feedGuardPages() {
		t.Run(page.name, func(t *testing.T) {
			var base [6]int
			var replyBytes int
			for _, replies := range []int{0, 1, 50} {
				fixture := page.fixture(t, 50)
				for i := range replies {
					fixture.Messages[i].Reply = &view.Reply{URL: view.MessageURL("acme", kernel.ID{9}), Handle: "owner", Text: "Original"}
				}
				measured := measureFeed(t, renderIn(t, "en", view.ChannelScreen("/static/css/app.css", fixture))).values
				if replies == 0 {
					base = measured
					if got := base[0] - 50*feedBudgetElementsSlope; got != int(page.limits[0].fixed) {
						t.Fatalf("fixed elements %d", got)
					}
				} else {
					if replies == 1 {
						replyBytes = measured[5] - base[5]
					}
					if measured[0]-base[0] != replies*replyBudgetElements || measured[5]-base[5] != replies*replyBytes {
						t.Fatal("reply markup is not linear")
					}
					for i := 1; i < 5; i++ {
						if measured[i] != base[i] {
							t.Fatalf("reply changed %s", page.limits[i].metric)
						}
					}
				}
				t.Logf("replies=%d elements=%d forms=%d submits=%d hx=%d selected=%d bytes=%d; fixed elements=%d per reply elements=%d bytes=%d", replies, measured[0], measured[1], measured[2], measured[3], measured[4], measured[5], base[0]-50*feedBudgetElementsSlope, replyBudgetElements, replyBytes)
			}
		})
	}
}

func TestReplyUnavailable(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		entry := conversation.Entry{Message: conversation.Message{Reply: &conversation.Reply{MessageID: kernel.ID{8}}}}
		markup := renderIn(t, lang, view.MessageItem(viewMessage(t.Context(), "acme", entry)))
		entry.Reply = nil
		plain := renderIn(t, lang, view.MessageItem(viewMessage(t.Context(), "acme", entry)))
		if measureFeed(t, markup).values[0]-measureFeed(t, plain).values[0] != replyBudgetElements {
			t.Fatal("unavailable preview changed the per-reply element count")
		}
		want := map[string]string{"en": "Original unavailable", "ja": "元の投稿は利用できません"}[lang]
		if !strings.Contains(markup, want) || strings.Contains(markup, `href="`+view.MessageURL("acme", kernel.ID{8})+`"`) {
			t.Fatal("unavailable target has a link or lacks localized text")
		}
	}
}

func TestReplyPreviewMarkup(t *testing.T) {
	for _, lang := range []string{"en", "ja"} {
		for _, display := range []string{"", "Alice"} {
			item := view.Message{Reply: &view.Reply{URL: view.MessageURL("acme", kernel.ID{9}), DisplayName: display, Handle: "owner", Text: "Original <text>"}}
			doc := measureFeed(t, renderIn(t, lang, view.MessageItem(item))).doc
			var link *html.Node
			for n := range doc.Descendants() {
				if n.Data == "a" && attr(n, "href") == item.Reply.URL {
					link = n
					break
				}
			}
			author := "@owner"
			if display != "" {
				author = display + " " + author
			}
			want := "In reply to " + author + ": Original <text>"
			if lang == "ja" {
				want = author + " への返信: Original <text>"
			}
			if link == nil || attr(link, "aria-label") != want {
				t.Fatalf("%s preview lacks localized accessible name", lang)
			}
			if countElements(link, nil) != 2 || countElements(link, func(n *html.Node) bool { return n.Data == "span" }) != 2 || hasHTMXAttribute(link) || countElements(link, hasHTMXAttribute) != 0 {
				t.Fatal("preview must contain only one link and two spans, without hx attributes")
			}
		}
	}
}
