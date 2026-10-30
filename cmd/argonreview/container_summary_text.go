package main

import (
	"strings"
	"unicode/utf8"
)

const (
	// summaryLineLimit keeps one hostile diagnostic from consuming the report.
	summaryLineLimit = 200
	// summarySourceLineLimit bounds a fragmented raw line before sanitising;
	// terminal sequences must not permit unlimited pending text per test.
	summarySourceLineLimit = 4096
	// summaryIndent separates all container text from the trusted report grammar.
	summaryIndent = "  | "
)

// summaryMaskName scans the whole identity with the tripwire's joined and
// sanitised passes. Truncation and newline splitting must not hide a match.
func summaryMaskName(name string) string {
	if verifyLogScan(name) > 0 {
		return "[name withheld]"
	}
	return name
}

func summaryName(name string) string {
	return summaryText(summaryMaskName(name))
}

// summaryMaskLines also catches shapes spanning diagnostic boundaries when the
// original identity boundaries are unavailable. Unlocated matches withhold all.
func summaryMaskLines(lines []string) []string {
	masked := append([]string(nil), lines...)
	for i := range lines {
		end := min(i+2, len(lines))
		if verifyLogScan(strings.Join(lines[i:end], "\n")) > 0 {
			for j := i; j < end; j++ {
				masked[j] = "[name withheld]"
			}
		}
	}
	// A located match must not hide a different shape spanning more lines.
	var unmasked []string
	for i, line := range lines {
		if masked[i] == line {
			unmasked = append(unmasked, line)
		}
	}
	if verifyLogScan(strings.Join(unmasked, "\n")) > 0 {
		for i := range masked {
			masked[i] = "[name withheld]"
		}
	}
	return masked
}

// summaryText removes terminal commands and invisible direction changes before
// capping bytes. It accepts one source line; even embedded newlines cannot
// escape the indent supplied by the caller.
func summaryText(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		if r == '\x1b' || r == '\u009b' || r == '\u009d' {
			mode := r
			if r == '\x1b' {
				if i == len(s) {
					break
				}
				if s[i] < 0x20 || s[i] > 0x7e {
					continue
				}
				mode = rune(s[i])
				i++
			}
			switch mode {
			case '[', '\u009b':
				for i < len(s) {
					c := s[i]
					i++
					if c >= 0x40 && c <= 0x7e {
						break
					}
				}
			case ']', 'P', '^', '_', '\u009d':
				for i < len(s) {
					c, size := utf8.DecodeRuneInString(s[i:])
					i += size
					if c == '\a' || c == '\u009c' {
						break
					}
					if c == '\x1b' && i < len(s) && s[i] == '\\' {
						i++
						break
					}
				}
			}
			continue
		}
		if r < 0x20 || r >= 0x7f && r <= 0x9f || r == 0x061c || r >= 0x200b && r <= 0x200f || r >= 0x2028 && r <= 0x202e || r >= 0x2060 && r <= 0x2064 || r >= 0x2066 && r <= 0x2069 || r == 0xfeff {
			continue
		}
		if out.Len()+utf8.RuneLen(r) > summaryLineLimit {
			break
		}
		out.WriteRune(r)
	}
	return out.String()
}

// summaryLines counts discarded lines too, so retention and report budgets can
// share an honest omission count without retaining an entire output stream.
// Shortening flags parallel retained lines, since omitted text is not displayed.
type summaryLines struct {
	lines                     []string
	total                     int
	shortened                 []bool
	partial                   string
	partialActive, partialCut bool
}

// add retains only the first lines; callers still scan the complete stream for
// failure facts and allowlist messages, independent of excerpt retention.
func (l *summaryLines) add(s string, limit int) {
	l.fragment(s, limit)
	l.end(limit)
}

// fragment joins test2json output fragments before identifying source lines,
// retaining only a bounded prefix even when a test never prints a newline.
func (l *summaryLines) fragment(s string, limit int) {
	for s != "" {
		chunk, rest, newline := strings.Cut(s, "\n")
		l.partialActive = true
		if len(l.lines) < limit {
			room := summarySourceLineLimit - len(l.partial)
			end := min(len(chunk), room)
			for end > 0 && end < len(chunk) && !utf8.RuneStart(chunk[end]) {
				end--
			}
			l.partial += chunk[:end]
			l.partialCut = l.partialCut || len(chunk) > room
		}
		if !newline {
			break
		}
		l.end(limit)
		s = rest
	}
}

// end also accounts for a final unterminated source line. Passing tests need
// no special flush policy; protocol finalisation handles all test identities.
func (l *summaryLines) end(limit int) {
	if !l.partialActive {
		return
	}
	l.total++
	if len(l.lines) < limit {
		text := summaryText(l.partial)
		l.shortened = append(l.shortened, l.partialCut || text != l.partial)
		l.lines = append(l.lines, text)
	}
	l.partial, l.partialActive, l.partialCut = "", false, false
}
