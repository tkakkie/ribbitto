package view

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"golang.org/x/net/html"
)

func quoteViewAttr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func quoteViewText(n *html.Node) string {
	var text strings.Builder
	for child := range n.Descendants() {
		if child.Type == html.TextNode {
			text.WriteString(child.Data)
		}
	}
	return text.String()
}

func quoteViewRenderItem(t *testing.T, quote *Quote) (string, *html.Node) {
	t.Helper()
	quotingMessageID := kernel.ID{1}
	var body bytes.Buffer
	if err := MessageItem(Message{
		ID:    quotingMessageID,
		Body:  "Reply body",
		Quote: quote,
	}).Render(t.Context(), &body); err != nil {
		t.Fatal(err)
	}
	raw := body.String()
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var figure *html.Node
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.Data == "figure" {
			if figure != nil {
				t.Fatal("multiple figure elements rendered")
			}
			figure = n
		}
	}
	return raw, figure
}

func TestMessageQuote(t *testing.T) {
	quotedMessageID := kernel.ID{7}
	quoteURL := MessageURL("acme", quotedMessageID)

	for _, tc := range []struct {
		name  string
		quote *Quote
		check func(*testing.T, string, *html.Node)
	}{
		{
			name:  "nil quote",
			quote: nil,
			check: func(t *testing.T, _ string, figure *html.Node) {
				if figure != nil {
					t.Fatal("rendered figure for nil quote")
				}
			},
		},
		{
			name:  "non-empty URL renders link",
			quote: &Quote{DisplayName: "Bob Example", Handle: "bob", Text: "Quoted excerpt", URL: quoteURL},
			check: func(t *testing.T, _ string, figure *html.Node) {
				if figure == nil {
					t.Fatal("missing quote figure")
				}
				links := 0
				for n := range figure.Descendants() {
					if n.Type == html.ElementNode && n.Data == "a" {
						links++
						href, ok := quoteViewAttr(n, "href")
						if !ok || href != quoteURL || quoteViewText(n) != "message.quote_link" {
							t.Fatalf("quote link href=%q text=%q, want %q and \"message.quote_link\"", href, quoteViewText(n), quoteURL)
						}
					}
				}
				if links != 1 {
					t.Fatalf("quote link count = %d, want 1", links)
				}
			},
		},
		{
			name:  "empty URL omits link",
			quote: &Quote{DisplayName: "Bob Example", Handle: "bob", Text: "Quoted excerpt", URL: ""},
			check: func(t *testing.T, _ string, figure *html.Node) {
				if figure == nil {
					t.Fatal("missing quote figure")
				}
				for n := range figure.Descendants() {
					if n.Type == html.ElementNode && n.Data == "a" {
						t.Fatalf("unexpected link in quote without URL: %+v", n.Attr)
					}
				}
			},
		},
		{
			name:  "text is HTML-escaped",
			quote: &Quote{DisplayName: "Bob Example", Handle: "bob", Text: "<b>escaped & safe</b>", URL: quoteURL},
			check: func(t *testing.T, raw string, figure *html.Node) {
				if figure == nil {
					t.Fatal("missing quote figure")
				}
				if !strings.Contains(raw, "&lt;b&gt;escaped &amp; safe&lt;/b&gt;") || strings.Contains(raw, "<b>escaped") {
					t.Fatalf("quote text was not HTML-escaped in raw output: %s", raw)
				}
				for n := range figure.Descendants() {
					if n.Type == html.ElementNode && n.Data == "blockquote" {
						for child := range n.Descendants() {
							if child.Type == html.ElementNode {
								t.Fatalf("blockquote contains unescaped element <%s>", child.Data)
							}
						}
						if got := quoteViewText(n); got != "<b>escaped & safe</b>" {
							t.Fatalf("blockquote text = %q, want %q", got, "<b>escaped & safe</b>")
						}
						return
					}
				}
				t.Fatal("missing blockquote")
			},
		},
		{
			name:  "blockquote has dir auto",
			quote: &Quote{DisplayName: "Bob Example", Handle: "bob", Text: "Quoted excerpt", URL: quoteURL},
			check: func(t *testing.T, _ string, figure *html.Node) {
				if figure == nil {
					t.Fatal("missing quote figure")
				}
				for n := range figure.Descendants() {
					if n.Type == html.ElementNode && n.Data == "blockquote" {
						dir, ok := quoteViewAttr(n, "dir")
						if !ok || dir != "auto" {
							t.Fatalf("blockquote dir = %q (%t), want \"auto\"", dir, ok)
						}
						if got := quoteViewText(n); got != "Quoted excerpt" {
							t.Fatalf("blockquote text = %q, want %q", got, "Quoted excerpt")
						}
						return
					}
				}
				t.Fatal("missing blockquote")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, figure := quoteViewRenderItem(t, tc.quote)
			tc.check(t, raw, figure)
		})
	}
}
