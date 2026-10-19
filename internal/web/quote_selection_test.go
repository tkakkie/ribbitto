package web

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/view"
	"golang.org/x/net/html"
)

func TestQuoteSelectionMarkup(t *testing.T) {
	entry := view.Message{ID: kernel.ID{1}, Body: "original"}
	var previous string
	for _, member := range []kernel.ID{{2}, {3}} {
		page := view.ChannelPage{MemberID: member, Organization: view.Organization{Slug: "acme"}, Current: view.Channel{ID: kernel.ID{4}}, Messages: []view.Message{entry}}
		var b bytes.Buffer
		if err := view.ChannelScreen("/static/css/app.css", page).Render(t.Context(), &b); err != nil {
			t.Fatal(err)
		}
		doc, err := html.Parse(&b)
		if err != nil {
			t.Fatal(err)
		}
		buttons, inputs, scripts := 0, 0, 0
		for n := range doc.Descendants() {
			if _, ok := attrOK(n, "data-quote-selection"); ok {
				buttons++
				if n.Data != "button" || attr(n, "type") != "submit" || attr(n, "form") != "quote-form" || attr(n, "name") != "quote" || attr(n, "value") != "01000000-0000-0000-0000-000000000000" {
					t.Fatal("selection button lost its submit contract")
				}
				if _, hidden := attrOK(n, "hidden"); !hidden {
					t.Fatal("selection button must start hidden")
				}
				if !strings.Contains(attr(n, "class"), "max-desk:min-h-11") || !strings.Contains(attr(n, "class"), "max-desk:min-w-11") {
					t.Fatal("selection button lost its phone target")
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
