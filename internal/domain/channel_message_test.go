package domain_test

import (
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/domain"
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
		t.Run(tc.input, func(t *testing.T) {
			got, err := domain.ValidateChannelName(tc.input)
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestValidateMessageBody(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"minimum", "a", "a"},
		{"invalid UTF-8", "a\xff", ""},
		{"line endings", "a\r\nb\rc\nd", "a\nb\nc\nd"},
		{"trailing tab", "hello\t", "hello"},
		{"whole body trim", "\t\r\n　a \r\n \tb　\n", "a \n \tb"},
		{"Unicode trim", "\u00a0\u2002hello\u2003\u3000", "hello"},
		{"interior spaces", "a\u00a0b", "a\u00a0b"},
		{"no NFC", "e\u0301", "e\u0301"},
		{"emoji joiner", "👩\u200d💻", "👩\u200d💻"},
		{"empty", "", ""}, {"whitespace only", " \t\r\n　\u00a0", ""},
		{"4000 code points", strings.Repeat("界", 4000), strings.Repeat("界", 4000)},
		{"trim before length", " " + strings.Repeat("界", 4000) + "\t", strings.Repeat("界", 4000)},
		{"4001 code points", strings.Repeat("界", 4001), ""},
		{"decomposed length", strings.Repeat("e\u0301", 2000) + "a", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.ValidateMessageBody(tc.input)
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	for _, r := range []rune("\x00\x01\x08\v\f\x0e\x1f\x7f\u0080\u0085\u009f\u2028\u2029") {
		t.Run("forbidden "+string(r), func(t *testing.T) {
			for _, input := range []string{string(r) + "hello", "hello" + string(r), "a" + string(r) + "b"} {
				if _, err := domain.ValidateMessageBody(input); err == nil {
					t.Fatalf("accepted forbidden character U+%04X before trimming", r)
				}
			}
		})
	}
}
