package conversation_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tkakkie/ribbitto/internal/conversation"
)

func TestShortTopicName(t *testing.T) {
	for _, tt := range []struct {
		name, input string
		// want is the exact result; rows with an empty want only check the
		// properties every result must have.
		want string
	}{
		{"empty", "", ""},
		{"short", "Planning", "Planning"},
		{"short Japanese", "雑談と開発", "雑談と開発"},
		{"exactly the limit", strings.Repeat("a", conversation.NoticeNameLength), strings.Repeat("a", conversation.NoticeNameLength)},
		{"one over", strings.Repeat("a", conversation.NoticeNameLength+1), strings.Repeat("a", conversation.NoticeNameLength-1) + "…"},
		{"space before the cut is dropped", strings.Repeat("a", 38) + " bcd", strings.Repeat("a", 38) + "…"},
		{"accented letters", strings.Repeat("é", 60), ""},
		{"Japanese", strings.Repeat("界", 80), ""},
		{"emoji", strings.Repeat("😀", 50), ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := conversation.ShortTopicName(tt.input)
			if tt.want != "" || tt.input == "" {
				if got != tt.want {
					t.Fatalf("ShortTopicName(%q) = %q, want %q", tt.input, got, tt.want)
				}
			}
			if !utf8.ValidString(got) {
				t.Fatalf("ShortTopicName(%q) = %q, which is not valid UTF-8", tt.input, got)
			}
			if n := utf8.RuneCountInString(got); n > conversation.NoticeNameLength {
				t.Fatalf("ShortTopicName(%q) has %d code points, want at most %d", tt.input, n, conversation.NoticeNameLength)
			}
			if got != tt.input && !strings.HasSuffix(got, "…") {
				t.Fatalf("ShortTopicName(%q) = %q, a shortened name must end with an ellipsis", tt.input, got)
			}
			if got != tt.input && !strings.HasPrefix(tt.input, strings.TrimSuffix(got, "…")) {
				t.Fatalf("ShortTopicName(%q) = %q, want a prefix of the name", tt.input, got)
			}
		})
	}
}
