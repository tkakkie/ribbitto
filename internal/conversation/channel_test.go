package conversation_test

import (
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
)

func TestValidateChannelName(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"a", "a"}, {"　 雑談 開発　 ", "雑談 開発"}, {"a　b", "a　b"},
		{" e\u0301 ", "é"}, {strings.Repeat("e\u0301", 80), strings.Repeat("é", 80)},
		{strings.Repeat("界", 80), strings.Repeat("界", 80)},
		{"", ""}, {"　 ", ""}, {strings.Repeat("界", 81), ""}, {"\xff", ""},
		{"a\x00", ""}, {"a\t", ""}, {"a\n", ""}, {"a\r", ""}, {"a\u007f", ""},
		{"a\u0085", ""}, {"a\u2028b", ""}, {"a\u2029b", ""}, {"a\u00a0b", ""},
		{"a\u200bb", ""}, {"a\u202eb", ""}, {"a\ufeffb", ""}, {"a\u00adb", ""},
	} {
		t.Run("channel/"+tc.input, func(t *testing.T) {
			got, err := conversation.ValidateChannelName(tc.input)
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
