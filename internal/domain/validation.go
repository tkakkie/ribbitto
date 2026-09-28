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

// reservedHandles would read as group mentions (@everyone, @here, …).
var reservedHandles = map[string]bool{"everyone": true, "here": true, "channel": true, "all": true}

// ValidateHandle returns the canonical lower-case handle: 2–32 characters,
// starting with a-z, ending with a-z or 0-9, with a-z, 0-9, _, . or - in
// between, and not a reserved word.
func ValidateHandle(value string) (string, error) {
	if !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
		return "", fmt.Errorf("handle must contain valid text without control characters")
	}
	value = strings.TrimSpace(value)
	// Reject non-ASCII before lower-casing: Unicode case mapping would turn
	// the Kelvin sign (U+212A) into "k" and let a look-alike through.
	for i := range len(value) {
		if value[i] >= utf8.RuneSelf {
			return "", fmt.Errorf("handle must contain only ASCII characters")
		}
	}
	value = strings.ToLower(value)
	if len(value) < 2 || len(value) > 32 || value[0] < 'a' || value[0] > 'z' || !isHandleEnd(value[len(value)-1]) {
		return "", fmt.Errorf("handle must contain 2–32 characters, start with a letter and end with a letter or digit")
	}
	for i := range len(value) {
		if c := value[i]; !isHandleEnd(c) && c != '_' && c != '.' && c != '-' {
			return "", fmt.Errorf("handle must contain only a-z, 0-9, _, . and -")
		}
	}
	if reservedHandles[value] {
		return "", fmt.Errorf("handle %q is reserved", value)
	}
	return value, nil
}

func isHandleEnd(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }

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
