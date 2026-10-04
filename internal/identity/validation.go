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

// ValidateDisplayName returns a trimmed NFC name of 1–50 printable Unicode
// code points that is not blank-looking (IsBlankLookingName).
func ValidateDisplayName(value string) (string, error) {
	value, err := validateText(value, 1, 50, "display name")
	if err == nil && IsBlankLookingName(value) {
		return "", fmt.Errorf("display name must contain a visible character")
	}
	return value, err
}

// IsBlankLookingName reports whether a name is made only of characters that
// show as nothing or as white space: the ASCII and ideographic spaces, the
// Hangul fillers U+3164, U+FFA0, U+115F and U+1160, the braille blank U+2800,
// and combining marks (Unicode category M), which have no base character to
// attach to when nothing else is there. It is a fixed list of code points
// and one category, not a judgement of how a font renders the name
// (docs/domain/names.md). The empty name is blank-looking.
func IsBlankLookingName(name string) bool {
	for _, r := range name {
		switch {
		case r == ' ', r == '\u3000', r == '\u3164', r == '\uffa0', r == '\u115f', r == '\u1160', r == '\u2800':
		case unicode.Is(unicode.M, r):
		default:
			return false
		}
	}
	return true
}

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
