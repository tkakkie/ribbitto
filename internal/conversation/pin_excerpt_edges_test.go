package conversation_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tkakkie/ribbitto/internal/conversation"
)

// The limit is counted in code points, whatever their width in bytes, and the
// ellipsis is one of the limit.
func TestPinExcerptLimitEdges(t *testing.T) {
	limit := conversation.PinExcerptLength
	for _, tt := range []struct {
		name, body, want string
	}{
		{"kanji at the limit", strings.Repeat("界", limit), strings.Repeat("界", limit)},
		{"kanji one over", strings.Repeat("界", limit+1), strings.Repeat("界", limit-1) + "…"},
		{"emoji at the limit", strings.Repeat("😀", limit), strings.Repeat("😀", limit)},
		{"emoji one over", strings.Repeat("😀", limit+1), strings.Repeat("😀", limit-1) + "…"},
		{"ASCII one over", strings.Repeat("a", limit+1), strings.Repeat("a", limit-1) + "…"},
		{"a word ends at the cut", strings.Repeat("a", limit-2) + " b" + strings.Repeat("c", 10), strings.Repeat("a", limit-2) + "…"},
		{"collapsed white space counts once", strings.Repeat("a", limit-3) + " \n\t b" + strings.Repeat("c", 10), strings.Repeat("a", limit-3) + " b" + "…"},
		{"empty", "", ""},
		{"only spaces", " \n\t ", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			entry := conversation.Entry{Message: conversation.Message{Format: conversation.FormatPlainText, Body: tt.body}}
			got := conversation.PinExcerpt(entry, "")
			if got != tt.want || !utf8.ValidString(got) || utf8.RuneCountInString(got) > limit {
				t.Fatalf("PinExcerpt(%q) = %q (%d code points), want %q", tt.body, got, utf8.RuneCountInString(got), tt.want)
			}
		})
	}
}
