package identity

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// ValidateEmail returns the trimmed, lower-case NFC address used for storage and lookup.
func ValidateEmail(value string) (string, error) {
	if !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
		return "", fmt.Errorf("email must contain valid text without control characters")
	}
	value = norm.NFC.String(strings.ToLower(strings.TrimSpace(value)))
	if strings.ContainsFunc(value, func(r rune) bool { return !unicode.IsPrint(r) || unicode.IsSpace(r) }) {
		return "", fmt.Errorf("email must contain only printable characters without spaces")
	}
	local, host, found := strings.Cut(value, "@")
	if !found || local == "" || host == "" || strings.Contains(host, "@") || len(value) > 254 {
		return "", fmt.Errorf("email must have one @ with nonempty parts and at most 254 bytes")
	}
	return value, nil
}

// ValidatePassword preserves the input and imposes only a 15–128 code point length.
func ValidatePassword(value string) (string, error) {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 15 || utf8.RuneCountInString(value) > 128 {
		return "", fmt.Errorf("password must contain 15–128 Unicode code points")
	}
	return value, nil
}
