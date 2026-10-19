package conversation_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
)

func TestValidateQuotePart(t *testing.T) {
	for _, tc := range []struct{ name, part, original, want string }{
		{"found", "part", "a part here", "part"},
		{"not found", "absent", "original", ""},
		{"CRLF", "a\r\nb", "a\nb", "a\nb"},
		{"trim", " \tpart\n", "a part here", "part"},
		{"empty", "", "original", ""},
		{"too long", strings.Repeat("é", 4001), strings.Repeat("é", 4001), ""},
		{"limit", strings.Repeat("é", 4000), strings.Repeat("é", 4000), strings.Repeat("é", 4000)},
		{"forbidden", "a\x00b", "a\x00b", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := conversation.ValidateQuotePart(tc.part, tc.original)
			if got != tc.want || (err != nil) != (tc.want == "") || err != nil && !errors.Is(err, conversation.ErrInvalidBody) {
				t.Fatalf("part = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
