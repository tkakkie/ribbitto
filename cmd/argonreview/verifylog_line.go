package main

import (
	"fmt"
	"strings"
)

func verifyLogRune(r rune) string {
	if verifyLogInvisible(r) {
		return fmt.Sprintf(`\u{%04X}`, r)
	}
	return string(r)
}

func verifyLogLine(source string) string {
	type token struct {
		text string
		end  int
	}
	var tokens []token
	size, overflow := len("| "), false
	verifyLogText(source, func(r rune, end int) {
		text := verifyLogRune(r)
		if size+len(text) > verifyLogDataBytes {
			overflow = true
		}
		if !overflow {
			tokens = append(tokens, token{text, end})
			size += len(text)
		}
	})
	suffix := ""
	if overflow {
		for {
			end := 0
			if len(tokens) > 0 {
				end = tokens[len(tokens)-1].end
			}
			suffix = fmt.Sprintf("[cut %d bytes]", len(source)-end)
			if size+len(suffix) <= verifyLogDataBytes {
				break
			}
			size -= len(tokens[len(tokens)-1].text)
			tokens = tokens[:len(tokens)-1]
		}
	}
	var b strings.Builder
	b.WriteString("| ")
	for _, t := range tokens {
		b.WriteString(t.text)
	}
	b.WriteString(suffix)
	return b.String()
}
