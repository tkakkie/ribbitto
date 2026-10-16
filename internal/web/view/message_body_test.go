package view

import (
	"strings"
	"testing"
)

// Links are checked against the allow-list again when rendering, whatever
// the view model holds: another scheme stays text, never an href.
func TestBodyLinksAreWebOnly(t *testing.T) {
	for _, tt := range []struct {
		url, want string
	}{
		{"https://example.com/a", `<a href="https://example.com/a" rel="noopener noreferrer" dir="auto"`},
		{"javascript:alert(1)", `<p dir="auto" class="whitespace-pre-wrap">javascript:alert(1)</p>`},
	} {
		entry := Message{Blocks: []BodyBlock{{Lines: [][]BodyInline{{{Kind: BodyLink, Text: tt.url}}}}}}
		var b strings.Builder
		if err := messageBody(entry).Render(t.Context(), &b); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), tt.want) {
			t.Errorf("%s rendered %s", tt.url, b.String())
		}
	}
}
