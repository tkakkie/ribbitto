package domain

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

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
