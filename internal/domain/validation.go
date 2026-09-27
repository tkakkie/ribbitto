package domain

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

// ValidateDisplayName returns a trimmed NFC name of 1–50 printable Unicode code points.
func ValidateDisplayName(value string) (string, error) {
	return validateText(value, 1, 50, "display name")
}

// ValidatePassword preserves the input and imposes only a 15–128 code point length.
func ValidatePassword(value string) (string, error) {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 15 || utf8.RuneCountInString(value) > 128 {
		return "", fmt.Errorf("password must contain 15–128 Unicode code points")
	}
	return value, nil
}

// ValidateOrganizationName returns a trimmed NFC name of 1–100 printable Unicode code points.
func ValidateOrganizationName(value string) (string, error) {
	return validateText(value, 1, 100, "organization name")
}

// ValidateSlug accepts 1–63 ASCII letters, digits or hyphens, with alphanumeric ends.
func ValidateSlug(value string) (string, error) {
	if len(value) < 1 || len(value) > 63 || value[0] == '-' || value[len(value)-1] == '-' {
		return "", fmt.Errorf("slug must contain 1–63 characters with alphanumeric ends")
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return "", fmt.Errorf("slug must contain only a-z, 0-9 and hyphens")
		}
	}
	return value, nil
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
