package web

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

// #2200 approves three elements per available reply and no htmx attributes.
const replyBudgetElements = 3

// #2200: measured with the English 50-message feed fixtures, @owner and Original.
const replyBudgetBytes = 329

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
						if replyBytes != replyBudgetBytes {
							t.Fatalf("per-reply bytes %d, want %d", replyBytes, replyBudgetBytes)
						}
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
	t.Run("nil", func(t *testing.T) {
		checkReplyUnavailable(t, nil)
	})
	t.Run("populated", func(t *testing.T) {
		// ReplyPreview keeps its text private; hydrate it through the real reader
		// before simulating unavailable data at the web boundary.
		pool := pgtest.New(t)
		f := conversationtest.OrganizationWithOwner(t, pool, "acme", "general")
		m := org.Membership{Organization: org.Organization{ID: f.OrganizationID}, Member: org.Member{ID: f.MemberID}}
		posting := fixturePoster(pool)
		original, err := posting.Send(t.Context(), m, f.Channel.ID, conversation.Send{Body: "Hidden target text"})
		if err != nil {
			t.Fatal(err)
		}
		sent, err := posting.Send(t.Context(), m, f.Channel.ID, conversation.Send{Body: "answer", ReplyTo: &original.Message.ID})
		if err != nil {
			t.Fatal(err)
		}
		entry, err := postgresServices(t, pool, nil, "", false).Messages.One(t.Context(), m, f.Channel.ID, sent.Message.EventSeq)
		if err != nil {
			t.Fatal(err)
		}
		preview := entry.ReplyPreview
		if preview == nil || preview.Text("") != "Hidden target text" {
			t.Fatal("fixture lacks target text")
		}
		preview.Available = false
		preview.Author = conversation.Reference{DisplayName: "Leaky", Handle: "leaky"}
		checkReplyUnavailable(t, preview)
	})
}

func checkReplyUnavailable(t *testing.T, preview *conversation.ReplyPreview) {
	t.Helper()
	for _, lang := range []string{"en", "ja"} {
		t.Run(lang, func(t *testing.T) {
			entry := conversation.Entry{Message: conversation.Message{Reply: &conversation.Reply{MessageID: kernel.ID{8}}}, ReplyPreview: preview}
			markup := renderIn(t, lang, view.MessageItem(viewMessage(t.Context(), "acme", entry)))
			entry.Reply = nil
			plain := renderIn(t, lang, view.MessageItem(viewMessage(t.Context(), "acme", entry)))
			if measureFeed(t, markup).values[0]-measureFeed(t, plain).values[0] != replyBudgetElements {
				t.Fatal("unavailable preview changed the per-reply element count")
			}
			// Other message controls contain links; inspect only the preview,
			// which is the direct child inserted before the message body.
			var previewMarkup bytes.Buffer
			for n := range measureFeed(t, markup).doc.Descendants() {
				if n.Parent != nil && n.Parent.Data == "li" && (n.Data == "a" || n.Data == "span") {
					if err := html.Render(&previewMarkup, n); err != nil {
						t.Fatal(err)
					}
				}
			}
			output := previewMarkup.String()
			if output == "" {
				t.Fatal("missing reply preview")
			}
			for _, forbidden := range []string{"<a", "href", "Leaky", "leaky", "Hidden target text"} {
				if strings.Contains(output, forbidden) {
					t.Errorf("unavailable preview leaked %q", forbidden)
				}
			}
			want := map[string]string{"en": "Reply to an unavailable message", "ja": "利用できない投稿への返信"}[lang]
			if !strings.Contains(output, want) {
				t.Fatal("unavailable preview lacks localized text")
			}
			if !strings.Contains(output, `<span aria-hidden="true">↳</span>`) {
				t.Fatal("unavailable preview lacks decorative reply cue")
			}
		})
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
			if link.FirstChild == nil || link.FirstChild.Type != html.TextNode || strings.TrimSpace(link.FirstChild.Data) != "↳" {
				t.Fatal("preview lacks a leading text reply cue")
			}
			if countElements(link, nil) != 2 || countElements(link, func(n *html.Node) bool {
				return n.Data == "span" && n.FirstChild != nil && n.FirstChild.Data == item.Reply.Text
			}) != 1 || countElements(link, func(n *html.Node) bool {
				return n.Data == "bdi" && n.FirstChild != nil && n.FirstChild.Data == author && attr(n, "dir") == ""
			}) != 1 || hasHTMXAttribute(link) || countElements(link, hasHTMXAttribute) != 0 {
				t.Fatal("preview must contain only one link, one author bdi and one text span, without hx attributes")
			}
		}
	}
}

func TestComposerReplyCost(t *testing.T) {
	for _, page := range feedGuardPages() {
		fixture := page.fixture(t, 50)
		base := measureFeed(t, renderIn(t, "en", view.ChannelScreen("/static/css/app.css", fixture))).values
		fixture.Reply = &view.Message{ID: kernel.ID{9}, Handle: "owner", Body: "Original"}
		selected := measureFeed(t, renderIn(t, "en", view.ChannelScreen("/static/css/app.css", fixture))).values
		want := [5]int{4, 0, 1, 0, 1}
		for i, delta := range want {
			if selected[i]-base[i] != delta {
				t.Errorf("%s composer target %s delta %d, want %d", page.name, page.limits[i].metric, selected[i]-base[i], delta)
			}
		}
		t.Logf("%s target deltas: elements=%d forms=%d submits=%d hx=%d selected=%d bytes=%d", page.name, selected[0]-base[0], selected[1]-base[1], selected[2]-base[2], selected[3]-base[3], selected[4]-base[4], selected[5]-base[5])
	}
	want := `<div id="composer-quote" hidden class="keyboard:order-first keyboard:w-full"><div id="composer-quote-description"></div></div>`
	if got := renderIn(t, "en", view.ComposerQuote(view.ChannelPage{}, false)); got != want {
		t.Fatalf("empty composer target markup changed: %s", got)
	}
	for _, lang := range []string{"en", "ja"} {
		page := feedFixture(t, 50)
		page.Reply = &view.Message{ID: kernel.ID{9}, Handle: "owner", Body: "Original"}
		markup := renderIn(t, lang, view.ChannelScreen("/static/css/app.css", page))
		if violations := checkMarkup(measureFeed(t, markup).doc, true); len(violations) != 0 {
			t.Errorf("%s reply markup: %v", lang, violations)
		}
		target := replyTargetMarkup(t, markup)
		if !strings.Contains(markup, `aria-describedby="message-help composer-quote-description composer-reply-description"`) {
			t.Errorf("%s body lacks target descriptions", lang)
		}
		for _, want := range []string{`id="composer-reply-description" role="status"`, `name="clear_reply" value="1" class="min-h-11 min-w-11`, map[string]string{"en": "In reply to @owner: Original", "ja": "@owner への返信: Original"}[lang]} {
			if !strings.Contains(target, want) {
				t.Errorf("%s target lacks %s", lang, want)
			}
		}
	}
}
