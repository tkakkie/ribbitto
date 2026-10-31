package main

import (
	"strings"
	"unicode/utf8"
)

func verifyLogInvisible(r rune) bool {
	return r < 0x20 || r >= 0x7f && r <= 0x9f || r == 0x061c ||
		r >= 0x200b && r <= 0x200f || r >= 0x2028 && r <= 0x202e ||
		r >= 0x2060 && r <= 0x2064 || r >= 0x2066 && r <= 0x2069 || r == 0xfeff
}

// verifyLogText walks the same terminal grammar as summaryText, without its
// display cap. end is a source byte offset, needed for honest cut notices.
// The differential corpus pins this grammar to the merged summary sanitiser.
func verifyLogText(s string, visit func(rune, int)) {
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		if r != '\x1b' && r != '\u009b' && r != '\u009d' {
			visit(r, i)
			continue
		}
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
	}
}

func verifyLogVisible(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	verifyLogText(s, func(r rune, _ int) {
		if !verifyLogInvisible(r) {
			b.WriteRune(r)
		}
	})
	return b.String()
}
