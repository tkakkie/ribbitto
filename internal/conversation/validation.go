package conversation

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

func validateText(value string, minLength, maxLength int, field string) (string, error) {
	if !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
		return "", fmt.Errorf("%s must contain valid text without control characters", field)
	}
	value = norm.NFC.String(strings.TrimSpace(value))
	length := utf8.RuneCountInString(value)
	// IsPrint allows only the ASCII space among spaces; also allow the
	// ideographic space, which Japanese names use between family and given name.
	if length < minLength || length > maxLength || strings.ContainsFunc(value, func(r rune) bool { return !unicode.IsPrint(r) && r != '\u3000' }) {
		return "", fmt.Errorf("%s must contain %d–%d printable Unicode code points", field, minLength, maxLength)
	}
	return value, nil
}
