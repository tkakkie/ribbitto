package view

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestComposerQuoteDescription(t *testing.T) {
	for _, tc := range []struct {
		name  string
		quote *Message
	}{
		{"empty", nil},
		{"selected", &Message{DisplayName: "Alice Example", Handle: "alice", Body: "Quoted excerpt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body bytes.Buffer
			if err := ComposerQuote(ChannelPage{Quote: tc.quote}, false).Render(t.Context(), &body); err != nil {
				t.Fatal(err)
			}
			doc, err := html.Parse(&body)
			if err != nil {
				t.Fatal(err)
			}
			for n := range doc.Descendants() {
				for _, a := range n.Attr {
					if a.Key != "id" || a.Val != "composer-quote-description" {
						continue
					}
					var text strings.Builder
					for child := range n.Descendants() {
						if child.Type == html.ElementNode && child.Data == "button" {
							t.Fatal("quote description includes a button")
						}
						if child.Type == html.TextNode {
							text.WriteString(child.Data)
						}
					}
					if tc.quote == nil {
						if text.Len() != 0 {
							t.Fatalf("empty quote description = %q", text.String())
						}
					} else {
						for _, want := range []string{tc.quote.DisplayName, "@" + tc.quote.Handle, tc.quote.Body} {
							if !strings.Contains(text.String(), want) {
								t.Errorf("description %q lacks %q", text.String(), want)
							}
						}
					}
					return
				}
			}
			t.Fatal("missing quote description")
		})
	}
}
