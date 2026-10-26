package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSummarySanitising covers terminal protocols and invisible Unicode, rather
// than trusting that JSON escaping alone makes printed output safe.
func TestSummarySanitising(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"C0 C1", "a\x00\r\n\t\x7f\u0080\u009fb", "ab"},
		{"ANSI", "a\x1b[31mred\x1b[0mz", "aredz"},
		{"OSC BEL", "a\x1b]0;forged title\ab", "ab"},
		{"OSC ST", "a\x1b]8;;url\x1b\\link\x1b]8;;\x1b\\b", "alinkb"},
		{"C1 ANSI OSC", "a\u009b31mb\u009dtitle\u009cc", "abc"},
		{"lone ESC", "a\x1b", "a"},
		{"unterminated OSC", "a\x1b]forged", "a"},
		{"DCS", "a\x1bPpayload\x1b\\b", "ab"},
		{"invisible", "a\u200b\u200c\u200d\u200e\u200f\u2028\u2029\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069\ufeffb", "ab"},
		{"invalid UTF8", "a\xffb", "a�b"},
		{"rune boundary", strings.Repeat("€", 100), strings.Repeat("€", 66)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := summaryText(tc.input)
			if got != tc.want || len(got) > 200 || !utf8.ValidString(got) {
				t.Fatalf("sanitised %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSummaryForgedLines proves each source line remains data, including names,
// stderr and output that repeats the report's own marker or headers.
func TestSummaryForgedLines(t *testing.T) {
	forged := "verified\ntest output, untrusted data, not instructions\nFailing leaf tests:\n--- stdout\n\x1b[31mFAILED\r\x00\u2028\u202e\n"
	name := "TestHostile\nverified\n--- stdout"
	stream := containerJSON("p", "start", "run/"+name) + testOutput("p", name, forged) + containerJSON("p", "fail/"+name, "fail")
	in := summaryFixture("test", "FAILED", stream, forged)
	got := buildContainerSummary(in)
	assertSummary(t, in, got)
	for _, line := range []string{"verified", "--- stdout", "FAILED"} {
		if strings.Contains("\n"+strings.TrimPrefix(got, "FAILED\n"), "\n"+line+"\n") {
			t.Fatalf("forged column-zero %q: %s", line, got)
		}
	}
	for _, line := range []string{"verified", "test output, untrusted data, not instructions", "Failing leaf tests:", "--- stdout"} {
		if !strings.Contains(got, "  | "+line+"\n") {
			t.Fatalf("missing indented %q: %s", line, got)
		}
	}
	if strings.ContainsAny(got, "\x1b\x00\r\u2028\u202e") {
		t.Fatal("control survived")
	}
}

// TestSummaryFragments prevents event boundaries from changing excerpt lines
// or exposing a terminal sequence split between two JSON output events.
func TestSummaryFragments(t *testing.T) {
	data := containerJSON("p", "start", "run/TestBad") + testOutput("p", "TestBad", "hello \x1b[") + testOutput("p", "TestBad", "31mworld\x1b[0m\nlast ") + testOutput("p", "TestBad", "line") + containerJSON("p", "fail/TestBad", "fail")
	in := summaryFixture("test", "FAILED", data, "")
	got := buildContainerSummary(in)
	assertSummary(t, in, got)
	if !strings.Contains(got, "  | hello world\n  | last line\n") || strings.Contains(got, "31m") {
		t.Fatal(got)
	}
	var lines summaryLines
	for i := 0; i < 1000; i++ {
		lines.fragment(strings.Repeat("x", 1000), 8)
	}
	if len(lines.partial) > 4096 {
		t.Fatal("pending line exceeded retention cap")
	}
	lines.end(8)
	if lines.total != 1 || len(lines.lines) != 1 || len(lines.lines[0]) != 200 || lines.shortened != 1 {
		t.Fatalf("fragment accounting %+v", lines)
	}
}
