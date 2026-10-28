package main

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestVerifyLogLineDifferential(t *testing.T) {
	corpus := []string{"plain", "\xff", "a\x00b\x7fc\u0080d\u202ee", "\x1b", "\x1b\u202ex", "\x1b[31mred\x1b[0m", "\u009b31mred", "\u009dtitle\u009cok", "\x1b]title\aok", "\x1bPtitle\x1b\\ok", "\x1b^title\aok", "\x1b_title\aok", "\x1b~ok", "\x1b[unterminated", "\x1b]unterminated", "\x1b[\xffmtext", "\x1b\x01x", "\u061c\u200b\u200f\u2028\u202e\u2060\u2064\u2066\u2069\ufeff😀"}
	for c := rune(0); c <= 0x9f; c++ {
		corpus = append(corpus, "a"+string(c)+"b")
	}
	remove := regexp.MustCompile(`\\u\{[0-9A-F]+\}`)
	for _, input := range corpus {
		got := strings.TrimPrefix(verifyLogLine(input), "| ")
		if remove.ReplaceAllString(got, "") != summaryText(input) {
			t.Fatalf("differential %q: %q vs %q", input, got, summaryText(input))
		}
	}
}

func TestVerifyLogLineBounds(t *testing.T) {
	for _, input := range []string{
		strings.Repeat("\x01", verifyLogDataBytes), strings.Repeat("😀", verifyLogDataBytes),
		strings.Repeat("a", verifyLogDataBytes), strings.Repeat("a", verifyLogDataBytes-len("| ")),
		strings.Repeat("a", verifyLogDataBytes-len("| ")+1), strings.Repeat("\u202e", verifyLogDataBytes),
	} {
		got := verifyLogLine(input)
		if len(got) > verifyLogDataBytes || !utf8.ValidString(got) || !strings.HasPrefix(got, "| ") {
			t.Fatalf("invalid line (%d): %q", len(got), got)
		}
		if len(input)+len("| ") <= verifyLogDataBytes && !strings.ContainsAny(input, "\x01\u202e") {
			if got != "| "+input {
				t.Fatal("line cut at inclusive boundary")
			}
			continue
		}
		marker := strings.LastIndex(got, "[cut ")
		if marker < 0 {
			t.Fatal("cut not marked")
		}
		var dropped int
		if _, err := fmt.Sscanf(got[marker:], "[cut %d bytes]", &dropped); err != nil {
			t.Fatal(err)
		}
		prefix := got[len("| "):marker]
		if strings.ContainsAny(input, "\x01\u202e") {
			if regexp.MustCompile(`^(\\u\{[0-9A-F]+\})*$`).FindString(prefix) != prefix {
				t.Fatal("partial control token")
			}
			unit := "\x01"
			if strings.ContainsRune(input, '\u202e') {
				unit = "\u202e"
			}
			shown := strings.Count(prefix, `\u{`) * len(unit)
			if dropped != len(input)-shown {
				t.Fatal("incorrect source-byte cut count")
			}
		} else if dropped != len(input)-len(prefix) {
			t.Fatal("incorrect rune cut count")
		}
		if input[0] == 'a' && len(prefix) < len(input) {
			next := len("| ") + len(prefix) + 1 + len(fmt.Sprintf("[cut %d bytes]", len(input)-len(prefix)-1))
			if next <= verifyLogDataBytes {
				t.Fatal("cut left room for another whole token")
			}
		}
	}
}
