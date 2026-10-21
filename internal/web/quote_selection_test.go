package web

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

func TestQuoteSelectionMarkup(t *testing.T) {
	entry := view.Message{ID: kernel.ID{1}, Body: "original"}
	var previous string
	for _, member := range []kernel.ID{{2}, {3}} {
		page := view.ChannelPage{MemberID: member, Organization: view.Organization{Slug: "acme"}, Current: view.Channel{ID: kernel.ID{4}}, Messages: []view.Message{entry}}
		doc, err := html.Parse(strings.NewReader(renderIn(t, "en", view.ChannelScreen("/static/css/app.css", page))))
		if err != nil {
			t.Fatal(err)
		}
		buttons, inputs, scripts := 0, 0, 0
		for n := range doc.Descendants() {
			if _, ok := attrOK(n, "data-quote-selection"); ok {
				buttons++
				if len(n.Attr) != 7 || n.Data != "button" || !plainMessageSubmit(n) || attr(n, "form") != "quote-form" || attr(n, "name") != "quote" || attr(n, "value") != "01000000-0000-0000-0000-000000000000" {
					t.Fatal("selection button lost its submit contract")
				}
				if _, hidden := attrOK(n, "hidden"); !hidden || attr(n, "aria-describedby") != "body-"+view.MessageDOMID(entry.ID) {
					t.Fatal("selection button must start hidden")
				}
				if attr(n, "class") != "self-start min-h-6 rounded-md px-2 text-caption text-brand hover:bg-hover max-desk:min-h-11 max-desk:min-w-11" || text(n) != "Quote selection" {
					t.Fatal("selection button lost its classes or accessible name")
				}
				if n.PrevSibling == nil || attr(n.PrevSibling, "id") != "body-"+view.MessageDOMID(entry.ID) {
					t.Fatal("selection button must immediately follow the body")
				}
				var button bytes.Buffer
				if err := html.Render(&button, n); err != nil {
					t.Fatal(err)
				}
				if previous != "" && button.String() != previous {
					t.Fatal("selection button varies by viewer")
				}
				previous = button.String()
			}
			if n.Data == "input" && attr(n, "name") == "quote_text" {
				inputs++
				_, disabled := attrOK(n, "disabled")
				if !disabled || attr(n, "type") != "hidden" || attr(n.Parent, "id") != "quote-form" || attr(n, "id") == "quote-text" {
					t.Fatal("selection input must be disabled in the separate quote form")
				}
			}
			if n.Data == "script" && attr(n, "src") == "/static/quote-selection-v1.js" {
				scripts++
			}
		}
		if buttons != 1 || inputs != 1 || scripts != 1 {
			t.Fatalf("selection buttons/inputs/scripts = %d/%d/%d, want 1/1/1", buttons, inputs, scripts)
		}
	}
}

type quoteSelectionFixture struct {
	name            string
	format          conversation.Format
	body, quoteText string
}

// Shared with the browser scenarios: expected text is independent of both the
// Go extractor and the selection serializer, so either changing breaks a test.
var quoteSelectionFixtures = []quoteSelectionFixture{
	{"plain", conversation.FormatPlainText, "**literal**\nline\n\nlast", "**literal**\nline\n\nlast"},
	{"mention", conversation.FormatChatV1, "hello <@MEMBER> again", "hello @owner again"},
	{"one-block-blank-lines", conversation.FormatChatV1, "first paragraph\n\nsecond paragraph", "first paragraph\n\nsecond paragraph"},
	{"emphasis", conversation.FormatChatV1, "before **bold** and _italic_ after", "before bold and italic after"},
	{"code", conversation.FormatChatV1, "before `x<y` after", "before x<y after"},
	{"fenced-code", conversation.FormatChatV1, "before\n```go\nx<y\n  z\n```\nafter", "before\nx<y\n  z\nafter"},
	{"link", conversation.FormatChatV1, "see https://example.com/path now", "see https://example.com/path now"},
	{"line-break", conversation.FormatChatV1, "first line\nsecond line", "first line\nsecond line"},
}

func (f quoteSelectionFixture) entry(member kernel.ID) conversation.Entry {
	entry := conversation.Entry{
		Message: conversation.Message{ID: kernel.ID{2}, Format: f.format,
			Body: strings.ReplaceAll(f.body, "MEMBER", realtime.FormatPayloadID(member))},
		References: map[kernel.ID]conversation.Reference{member: {Handle: "owner", DisplayName: "acme"}},
	}
	if f.format == conversation.FormatChatV1 {
		entry.Blocks = conversation.ParseChatV1(entry.Body)
	}
	return entry
}

func TestQuoteSelectionFixtures(t *testing.T) {
	for _, fixture := range quoteSelectionFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			entry := fixture.entry(kernel.ID{1})
			text := entry.Text("@unknown")
			if !strings.Contains(text, fixture.quoteText) || text != fixture.quoteText {
				t.Fatalf("Entry.Text = %q, want quote_text %q", text, fixture.quoteText)
			}
			markup := streamTestMarkup(t, view.LiveMessageItem(viewMessage("acme", entry)))
			if fixture.name == "mention" && !strings.Contains(string(markup), `data-quote-handle="owner"`) {
				t.Fatal("resolved mention lacks its viewer-independent extraction handle")
			}
		})
	}
}
