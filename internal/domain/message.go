package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Message is a plain-text post identified by ID. EventSeq orders it within its organisation.
type Message struct {
	ID, OrganizationID, ChannelID, MemberID ID
	Body                                    string
	EventSeq                                int64
	CreatedAt                               time.Time
}

// ValidateMessageBody normalizes line endings and trims the whole plain-text body.
// It preserves other text, including decomposed characters and emoji joiners.
func ValidateMessageBody(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("message body must contain valid UTF-8")
	}
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	if strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\t' || r == '\u2028' || r == '\u2029' || isBidiEmbeddingOrOverride(r)
	}) {
		return "", fmt.Errorf("message body contains forbidden control characters or separators")
	}
	value = strings.TrimSpace(value)
	if length := utf8.RuneCountInString(value); length < 1 || length > 4000 {
		return "", fmt.Errorf("message body must contain 1–4000 Unicode code points")
	}
	return value, nil
}

// isBidiEmbeddingOrOverride reports LRE, RLE, PDF, LRO and RLO (U+202A–U+202E).
// They can make stored text read differently from how it is stored (for
// example a spoofed URL), and Unicode discourages them in new text. The
// isolates LRI, RLI, FSI and PDI (U+2066–U+2069), and marks such as LRM,
// RLM and ALM, are allowed on purpose: they are the recommended way to mix
// directions in plain text, and views contain them with dir="auto" or <bdi>.
func isBidiEmbeddingOrOverride(r rune) bool { return r >= '\u202a' && r <= '\u202e' }
